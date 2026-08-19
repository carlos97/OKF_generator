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
//     Parametrizarlos solo sirve para que alguien cambie un valor, RabbitMQ
//     responda PRECONDITION_FAILED (406) al redeclarar y los tres servicios
//     entren en crash-loop con `docker compose down -v` como unica salida.
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

// DeclareTopology crea exchanges y colas. La ejecuta un servicio one-shot; si
// una cola ya existe con argumentos distintos, RabbitMQ devuelve 406 y aqui se
// aborta con un mensaje explicito en vez de dejar a los servicios en bucle.
func DeclareTopology(conn *amqp.Connection) error {
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()

	for _, e := range []string{ExchangeJobs, ExchangeRetry, ExchangeDLX} {
		if err := ch.ExchangeDeclare(e, "direct", true, false, false, false, nil); err != nil {
			return fmt.Errorf("declarar exchange %s: %w", e, err)
		}
	}

	// Cola de trabajo. Sin x-delivery-limit: el presupuesto de intentos lo
	// gobierna la base de datos. Los nack sin requeue terminan en la DLQ; sin
	// estos argumentos RabbitMQ los descartaria silenciosamente.
	if _, err := ch.QueueDeclare(QueueJobs, true, false, false, false, amqp.Table{
		"x-dead-letter-exchange":    ExchangeDLX,
		"x-dead-letter-routing-key": RoutingKeyConvert,
	}); err != nil {
		return fmt.Errorf("declarar %s (posible PRECONDITION_FAILED por argumentos distintos; use 'docker compose down -v'): %w", QueueJobs, err)
	}
	if err := ch.QueueBind(QueueJobs, RoutingKeyConvert, ExchangeJobs, false, nil); err != nil {
		return err
	}

	// Cola de espera: sin consumidores. Los mensajes expiran por TTL DE COLA
	// (no por mensaje) y el dead-letter-exchange los devuelve a la cola de
	// trabajo. Al ser TTL de cola, todos los mensajes tienen el mismo plazo y
	// no puede haber bloqueo de cabecera.
	if _, err := ch.QueueDeclare(QueueRetry, true, false, false, false, amqp.Table{
		"x-message-ttl":             int32(RetryTTLMillis),
		"x-dead-letter-exchange":    ExchangeJobs,
		"x-dead-letter-routing-key": RoutingKeyConvert,
	}); err != nil {
		return fmt.Errorf("declarar %s: %w", QueueRetry, err)
	}
	if err := ch.QueueBind(QueueRetry, RoutingKeyConvert, ExchangeRetry, false, nil); err != nil {
		return err
	}

	if _, err := ch.QueueDeclare(QueueDLQ, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declarar %s: %w", QueueDLQ, err)
	}
	if err := ch.QueueBind(QueueDLQ, RoutingKeyConvert, ExchangeDLX, false, nil); err != nil {
		return err
	}
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
