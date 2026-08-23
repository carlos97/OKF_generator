// Package queue encapsula RabbitMQ.
//
// Decisiones de topologia y su porque:
//
//   - Colas CLASSIC DURABLE, no quorum. Con un solo nodo, quorum no aporta nada
//     y sus defaults cambian entre versiones (en 4.x las quorum traen
//     delivery-limit por defecto), lo que alteraria el comportamiento de
//     reintentos sin que nadie tocara el codigo.
//   - El contador de intentos vive en jobs.attempt (Postgres), NO en
//     x-delivery-limit. Republicar a una cola de espera crea un mensaje NUEVO y
//     reinicia cualquier contador del broker, con lo que un limite de entregas
//     nunca se dispararia para fallos de aplicacion y la DLQ jamas recibiria
//     nada por esa via.
//   - UN SOLO nivel de espera. Varias colas con TTL POR MENSAJE producen
//     head-of-line blocking: RabbitMQ solo expira el mensaje que esta en la
//     CABEZA, asi que un mensaje de 60 s delante bloquea a los de 5 s que van
//     detras y el backoff observado no se corresponde con el disenado.
//   - Los nombres y argumentos son CONSTANTES de Go, no variables de entorno.
//     Parametrizarlos solo sirve para que alguien cambie un valor y RabbitMQ
//     responda PRECONDITION_FAILED (406) al redeclarar. Ese 406 ya no es
//     fatal: DeclareTopology recrea la cola incompatible si esta vacia (ver
//     mas abajo), pero con los argumentos en el entorno el desajuste seria
//     permanente en cuanto la cola tuviera mensajes.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/uniandes-isis4426/okfp/internal/domain"
)

const (
	ExchangeJobs  = "okf.jobs"
	ExchangeRetry = "okf.retry"
	ExchangeDLX   = "okf.dlx"

	QueueJobs  = "okf.jobs.q"
	QueueRetry = "okf.retry.30s"
	QueueDLQ   = "okf.jobs.dlq"

	RoutingKeyConvert = "job.convert"

	// Retardo del unico nivel de espera. Con max_attempts=3 un backoff
	// escalonado no aporta nada observable a esta escala.
	RetryTTLMillis = 30000
)

type Publisher struct {
	conn *amqp.Connection
	mu   sync.Mutex
	ch   *amqp.Channel
	url  string

	// blocked se activa cuando el broker levanta una alarma de recursos y
	// bloquea a los publicadores. Sin esto, Publish se queda esperando sobre el
	// socket (el contexto no aborta una escritura ya en curso) y el handler de
	// carga deja de responder, que es exactamente lo contrario de la asincronia
	// que se quiere demostrar.
	blockedMu sync.RWMutex
	blocked   bool
}

func Dial(url string) (*amqp.Connection, error) {
	conn, err := amqp.DialConfig(url, amqp.Config{
		Heartbeat: 10 * time.Second,
		Locale:    "en_US",
	})
	if err != nil {
		return nil, fmt.Errorf("conectar a rabbitmq: %w", err)
	}
	return conn, nil
}

func NewPublisher(url string) (*Publisher, error) {
	conn, err := Dial(url)
	if err != nil {
		return nil, err
	}
	p := &Publisher{conn: conn, url: url}

	blockings := conn.NotifyBlocked(make(chan amqp.Blocking, 4))
	go func() {
		for b := range blockings {
			p.blockedMu.Lock()
			p.blocked = b.Active
			p.blockedMu.Unlock()
		}
	}()

	if err := p.openChannel(); err != nil {
		conn.Close()
		return nil, err
	}
	return p, nil
}

func (p *Publisher) openChannel() error {
	ch, err := p.conn.Channel()
	if err != nil {
		return fmt.Errorf("abrir canal: %w", err)
	}
	// Publisher confirms: sin ellos no se puede saber si el mensaje llego, y el
	// trabajo quedaria en cola para siempre sin que nadie lo detectase.
	if err := ch.Confirm(false); err != nil {
		ch.Close()
		return fmt.Errorf("activar confirms: %w", err)
	}
	p.ch = ch
	return nil
}

func (p *Publisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ch != nil {
		_ = p.ch.Close()
	}
	return p.conn.Close()
}

// DeclareTopology crea exchanges y colas. La ejecuta un servicio one-shot.
//
// Cada declaracion abre su PROPIO canal. RabbitMQ cierra el canal en cuanto
// responde PRECONDITION_FAILED (406), asi que compartir uno dejaria las
// declaraciones siguientes fallando con "channel/connection is not open" y el
// error visible ya no seria el que importa.
//
// Cuando una cola existe con argumentos distintos a los de este fichero -- el
// caso tipico es un volumen `rabbitdata` creado por una version anterior del
// codigo, sin `x-dead-letter-exchange` -- la topologia se REPARA en el sitio en
// lugar de exigir `docker compose down -v`, que se llevaria por delante tambien
// Postgres y MinIO. La reparacion solo procede si la cola esta VACIA, y esa
// condicion no es una precaucion generica: el barredor unicamente republica
// trabajos con enqueued_confirmed_at NULL, de modo que borrar una cola con
// mensajes ya confirmados perderia trabajo que nada volveria a recuperar.
func DeclareTopology(conn *amqp.Connection) error {
	for _, e := range []string{ExchangeJobs, ExchangeRetry, ExchangeDLX} {
		if err := declareExchange(conn, e); err != nil {
			return err
		}
	}

	// Cola de trabajo: sin x-delivery-limit, porque el presupuesto de intentos
	// lo gobierna la base de datos. Los nack sin requeue terminan en la DLQ;
	// sin estos argumentos RabbitMQ los descartaria silenciosamente.
	//
	// Cola de espera: sin consumidores. Los mensajes expiran por TTL DE COLA (no
	// por mensaje) y el dead-letter-exchange los devuelve a la cola de trabajo.
	// Al ser TTL de cola todos comparten plazo y no hay bloqueo de cabecera.
	for _, q := range []struct {
		name     string
		exchange string
		args     amqp.Table
	}{
		{QueueJobs, ExchangeJobs, amqp.Table{
			"x-dead-letter-exchange":    ExchangeDLX,
			"x-dead-letter-routing-key": RoutingKeyConvert,
		}},
		{QueueRetry, ExchangeRetry, amqp.Table{
			"x-message-ttl":             int32(RetryTTLMillis),
			"x-dead-letter-exchange":    ExchangeJobs,
			"x-dead-letter-routing-key": RoutingKeyConvert,
		}},
		{QueueDLQ, ExchangeDLX, nil},
	} {
		if err := declareQueue(conn, q.name, q.args); err != nil {
			return err
		}
		// La vinculacion se hace junto a su declaracion y no en un bucle
		// aparte: si la cola acaba de recrearse, sus vinculaciones anteriores
		// desaparecieron con ella.
		if err := withChannel(conn, func(ch *amqp.Channel) error {
			return ch.QueueBind(q.name, RoutingKeyConvert, q.exchange, false, nil)
		}); err != nil {
			return fmt.Errorf("vincular %s a %s: %w", q.name, q.exchange, err)
		}
	}
	return nil
}

// withChannel ejecuta fn sobre un canal recien abierto y lo cierra al salir.
func withChannel(conn *amqp.Connection, fn func(*amqp.Channel) error) error {
	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("abrir canal: %w", err)
	}
	defer ch.Close()
	return fn(ch)
}

// isPreconditionFailed distingue "ya existe con otra definicion" (406) de un
// fallo de red o de permisos. Solo el primero se arregla borrando y volviendo a
// declarar; tratar los demas igual esconderia problemas reales.
func isPreconditionFailed(err error) bool {
	var aerr *amqp.Error
	return errors.As(err, &aerr) && aerr.Code == amqp.PreconditionFailed
}

func declareExchange(conn *amqp.Connection, name string) error {
	declare := func(ch *amqp.Channel) error {
		return ch.ExchangeDeclare(name, "direct", true, false, false, false, nil)
	}

	err := withChannel(conn, declare)
	if err == nil {
		return nil
	}
	if !isPreconditionFailed(err) {
		return fmt.Errorf("declarar exchange %s: %w", name, err)
	}

	// Un exchange no almacena mensajes, asi que recrearlo no puede perder
	// trabajo. Las unicas vinculaciones que le importan al sistema son las de
	// esta misma funcion y se rehacen a continuacion.
	mismatch := err
	if err := withChannel(conn, func(ch *amqp.Channel) error {
		return ch.ExchangeDelete(name, false, false)
	}); err != nil {
		return fmt.Errorf("el exchange %s existe con otra definicion (%v) y no se pudo borrar: %w", name, mismatch, err)
	}
	if err := withChannel(conn, declare); err != nil {
		return fmt.Errorf("redeclarar exchange %s: %w", name, err)
	}
	fmt.Printf("topologia: exchange %s recreado (existia con otra definicion: %v)\n", name, mismatch)
	return nil
}

func declareQueue(conn *amqp.Connection, name string, args amqp.Table) error {
	declare := func(ch *amqp.Channel) error {
		_, err := ch.QueueDeclare(name, true, false, false, false, args)
		return err
	}

	err := withChannel(conn, declare)
	if err == nil {
		return nil
	}
	if !isPreconditionFailed(err) {
		return fmt.Errorf("declarar %s: %w", name, err)
	}

	// ifEmpty=true es la salvaguarda, y la evalua el broker de forma atomica: si
	// quedan mensajes, el borrado se rechaza y aqui se aborta con instrucciones
	// en vez de tirar trabajo a la basura en silencio.
	mismatch := err
	if err := withChannel(conn, func(ch *amqp.Channel) error {
		_, err := ch.QueueDelete(name, false, true, false)
		return err
	}); err != nil {
		return fmt.Errorf("la cola %s existe con argumentos incompatibles (%v) y no se puede recrear porque no esta vacia; "+
			"deje que los workers la vacien y repita `docker compose up topology`, o descarte su contenido con "+
			"`docker compose exec rabbitmq rabbitmqctl delete_queue %s`: %w", name, mismatch, name, err)
	}
	if err := withChannel(conn, declare); err != nil {
		return fmt.Errorf("redeclarar %s: %w", name, err)
	}
	fmt.Printf("topologia: cola %s recreada (existia con argumentos incompatibles: %v)\n", name, mismatch)
	return nil
}

// Publish envia el trabajo y espera el confirm del broker.
func (p *Publisher) Publish(ctx context.Context, msg domain.JobMessage) error {
	return p.publishTo(ctx, ExchangeJobs, msg)
}

// PublishRetry envia el trabajo a la cola de espera. Volvera solo a la cola de
// trabajo cuando expire el TTL.
func (p *Publisher) PublishRetry(ctx context.Context, msg domain.JobMessage) error {
	return p.publishTo(ctx, ExchangeRetry, msg)
}

// PublishDLQ archiva un trabajo definitivamente fallido para inspeccion.
func (p *Publisher) PublishDLQ(ctx context.Context, msg domain.JobMessage) error {
	return p.publishTo(ctx, ExchangeDLX, msg)
}

func (p *Publisher) publishTo(ctx context.Context, exchange string, msg domain.JobMessage) error {
	p.blockedMu.RLock()
	blocked := p.blocked
	p.blockedMu.RUnlock()
	if blocked {
		// Cortocircuito: mejor un 503 inmediato y honesto que un handler colgado.
		return domain.ErrUnavailable.WithMessage("la cola de mensajes esta aplicando control de flujo")
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.ch == nil || p.ch.IsClosed() {
		if err := p.openChannel(); err != nil {
			return err
		}
	}

	pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	conf, err := p.ch.PublishWithDeferredConfirmWithContext(pubCtx, exchange, RoutingKeyConvert, false, false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			MessageId:    msg.JobID.String(),
			Timestamp:    time.Now().UTC(),
			Body:         body,
		})
	if err != nil {
		return fmt.Errorf("publicar en %s: %w", exchange, err)
	}

	ok, err := conf.WaitContext(pubCtx)
	if err != nil {
		return fmt.Errorf("esperar confirm: %w", err)
	}
	if !ok {
		return errors.New("el broker rechazo el mensaje (nack)")
	}
	return nil
}

// --- Consumo ----------------------------------------------------------------

// Decision es lo que el consumidor devuelve por cada entrega.
type Decision int

const (
	// Ack: la entrega queda resuelta (procesada, duplicada o descartada).
	Ack Decision = iota
	// NackDrop: la entrega no es procesable y va a la DLQ. Se usa cuando la
	// fila del trabajo no existe. Nunca se hace ack silencioso en ese caso:
	// seria perder trabajo sin dejar rastro.
	NackDrop
)

type Handler func(ctx context.Context, msg domain.JobMessage) Decision

type Consumer struct {
	conn     *amqp.Connection
	ch       *amqp.Channel
	prefetch int
	workers  int
	tag      string
}

func NewConsumer(url string, prefetch, workers int, tag string) (*Consumer, error) {
	if prefetch < 1 {
		return nil, fmt.Errorf("prefetch debe ser al menos 1")
	}
	if workers < 1 {
		return nil, fmt.Errorf("concurrencia debe ser al menos 1")
	}
	conn, err := Dial(url)
	if err != nil {
		return nil, err
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, err
	}
	// Se reserva como maximo una entrega por ranura. Asi WORKER_CONCURRENCY
	// limita trabajo real en vuelo, sin dejar ranuras ociosas cuando PREFETCH
	// quedo con su default de 1.
	if prefetch < workers {
		prefetch = workers
	}
	if err := ch.Qos(prefetch, 0, false); err != nil {
		ch.Close()
		conn.Close()
		return nil, err
	}
	return &Consumer{conn: conn, ch: ch, prefetch: prefetch, workers: workers, tag: tag}, nil
}

func (c *Consumer) Close() error {
	if c.ch != nil {
		_ = c.ch.Close()
	}
	return c.conn.Close()
}

// Prefetch devuelve el credito efectivo, que puede crecer para no estrangular
// una concurrencia configurada mayor que el prefetch solicitado.
func (c *Consumer) Prefetch() int { return c.prefetch }

// Consume bloquea hasta que ctx se cancela.
//
// El contexto que se pasa al handler NO es el de apagado: el trabajo en vuelo
// debe poder terminar dentro del stop_grace_period. Cancelar el trabajo al
// recibir SIGTERM haria que cada reinicio de contenedor abortase conversiones a
// medias, multiplicase los intentos y el usuario viese "reintentando 2/3" sin
// causa aparente.
func (c *Consumer) Consume(ctx context.Context, workCtx context.Context, h Handler) error {
	deliveries, err := c.ch.Consume(QueueJobs, c.tag, false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consumir %s: %w", QueueJobs, err)
	}

	closed := c.conn.NotifyClose(make(chan *amqp.Error, 1))
	var wg sync.WaitGroup
	// El semaforo es la autoridad de concurrencia local. RabbitMQ puede haber
	// entregado hasta prefetch mensajes, pero nunca mas de workers se ejecutan.
	slots := make(chan struct{}, c.workers)

	for {
		select {
		case <-ctx.Done():
			// Dejar de recibir mensajes nuevos y esperar a los que estan en vuelo.
			_ = c.ch.Cancel(c.tag, false)
			wg.Wait()
			return nil

		case err := <-closed:
			wg.Wait()
			if err != nil {
				return fmt.Errorf("conexion con rabbitmq cerrada: %w", err)
			}
			return nil

		case d, ok := <-deliveries:
			if !ok {
				wg.Wait()
				return nil
			}
			wg.Add(1)
			go func(d amqp.Delivery) {
				defer wg.Done()
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					// El consumo ya se esta deteniendo. Esta entrega nunca llego
					// al handler, asi que vuelve a la cola para otra replica.
					_ = d.Nack(false, true)
					return
				}
				defer func() { <-slots }()
				handle(workCtx, d, h)
			}(d)
		}
	}
}

func handle(ctx context.Context, d amqp.Delivery, h Handler) {
	var msg domain.JobMessage
	if err := json.Unmarshal(d.Body, &msg); err != nil {
		// Un mensaje ilegible no mejora reintentando: a la DLQ.
		_ = d.Nack(false, false)
		return
	}
	switch h(ctx, msg) {
	case NackDrop:
		_ = d.Nack(false, false)
	default:
		_ = d.Ack(false)
	}
}

// QueueDepth se usa en la demostracion para enseñar la cola vaciandose al
// escalar workers.
func QueueDepth(conn *amqp.Connection, name string) (int, error) {
	ch, err := conn.Channel()
	if err != nil {
		return 0, err
	}
	defer ch.Close()
	q, err := ch.QueueDeclarePassive(name, true, false, false, false, nil)
	if err != nil {
		return 0, err
	}
	return q.Messages, nil
}
