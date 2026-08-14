# ---------------------------------------------------------------------------
# Comprueba las seis condiciones verificables sobre un despliegue en marcha.
#
#   docker compose up --build -d
#   .\scripts\verify-conditions.ps1
#
# Devuelve codigo de salida 0 si las seis pasan. Escrito para Windows
# PowerShell 5.1, que es la consola por defecto de Windows 10: por eso se usa
# curl.exe explicitamente (curl a secas es un alias de Invoke-WebRequest y no
# acepta -F ni -w) y no se usa Invoke-RestMethod -Form, que no existe antes de
# PowerShell 6.2.
# ---------------------------------------------------------------------------

$ErrorActionPreference = 'Stop'

$API  = if ($env:OKF_API) { $env:OKF_API } else { 'http://localhost:8080/api/v1' }
$root = Split-Path -Parent $PSScriptRoot
$fail = 0

function Section($n, $text) {
    Write-Host ''
    Write-Host "=== $n · $text ===" -ForegroundColor Cyan
}

function Pass($text) { Write-Host "  [OK]   $text" -ForegroundColor Green }
function Fail($text) { Write-Host "  [FALLA] $text" -ForegroundColor Red; $script:fail++ }

# WaitForAPI espera a que la API acepte peticiones antes de empezar.
#
# Sin esta espera, ejecutar el script justo despues de `docker compose up -d`
# hace que las primeras peticiones devuelvan un cuerpo vacio y el script muera
# con "Primitivo JSON no valido" en la primera llamada, lo que parece un fallo
# del sistema cuando en realidad el contenedor aun estaba arrancando. Un sleep
# fijo no sirve: el tiempo de arranque depende de la maquina.
function WaitForAPI($timeoutSec = 90) {
    $deadline = (Get-Date).AddSeconds($timeoutSec)

    # Se sondea una ruta REAL de la API bajo /api, y no /readyz.
    #
    # /healthz y /readyz viven en la raiz del servicio api, que nginx no proxea:
    # una peticion a http://localhost:8080/readyz cae en el `try_files ...
    # /index.html` del SPA y devuelve 200 aunque la API este muerta. Un sondeo
    # asi no comprueba nada y abre la puerta al instante, que es exactamente
    # como este script volvio a fallar despues de "arreglarlo".
    #
    # GET /api/v1/jobs sin token tiene que devolver 401: eso solo puede
    # responderlo la API. Mientras el contenedor no acepte conexiones, nginx
    # devuelve 502 (o curl no conecta y da 000).
    while ((Get-Date) -lt $deadline) {
        $code = curl.exe -s -o NUL -w "%{http_code}" --max-time 5 "$API/jobs" 2>$null
        if ($code -eq '401') { return }
        Start-Sleep -Milliseconds 700
    }
    throw "la API no respondio en $timeoutSec s (ultimo codigo: $code). Compruebe: docker compose ps"
}

function Login($email, $password) {
    # El cuerpo JSON se pasa POR FICHERO (-d "@ruta") y no en la linea de
    # comandos.
    #
    # Windows PowerShell 5.1 no escapa las comillas dobles al construir la linea
    # de comandos de un ejecutable nativo: `-d '{"email":"..."}'` llega a
    # curl.exe con las comillas comidas y la API responde 400 "el cuerpo no es
    # JSON valido". Es el mismo motivo por el que este script usa curl.exe y no
    # Invoke-RestMethod -Form. Con @fichero el contenido no pasa por el parser
    # de la linea de comandos y el problema desaparece.
    $body = @{ email = $email; password = $password } | ConvertTo-Json -Compress
    $tmp  = [System.IO.Path]::GetTempFileName()
    try {
        # Sin BOM: un BOM UTF-8 al principio del cuerpo tambien invalida el JSON.
        [System.IO.File]::WriteAllText($tmp, $body, (New-Object System.Text.UTF8Encoding($false)))
        $res = curl.exe -s -X POST "$API/auth/login" `
            -H "Content-Type: application/json" -d "@$tmp"
    } finally {
        Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue
    }
    $obj = $res | ConvertFrom-Json
    if (-not $obj.token) { throw "no se pudo autenticar $email : $res" }
    return $obj.token
}

function Upload($token, $file) {
    $path = Join-Path $root $file
    if (-not (Test-Path $path)) { throw "no existe $path" }
    $res = curl.exe -s -X POST "$API/documents" -H "Authorization: Bearer $token" -F "file=@$path"
    return ($res | ConvertFrom-Json)
}

function WaitJob($token, $jobId, $timeoutSec = 180) {
    $deadline = (Get-Date).AddSeconds($timeoutSec)
    while ((Get-Date) -lt $deadline) {
        $job = curl.exe -s "$API/jobs/$jobId" -H "Authorization: Bearer $token" | ConvertFrom-Json
        if (@('succeeded','invalid','failed','dead','canceled') -contains $job.status) { return $job }
        Start-Sleep -Milliseconds 700
    }
    throw "el trabajo $jobId no termino en $timeoutSec s"
}

function StatusOf($token, $url) {
    return (curl.exe -s -o NUL -w "%{http_code}" $url -H "Authorization: Bearer $token")
}

function PostStatusOf($token, $url) {
    return (curl.exe -s -o NUL -w "%{http_code}" -X POST $url -H "Authorization: Bearer $token")
}

# CurlText devuelve el cuerpo como UNA cadena.
#
# curl.exe visto desde PowerShell devuelve un ARRAY de lineas, no un string. Con
# un array, `-match` no captura: actua como filtro de elementos y deja $Matches
# sin rellenar, asi que una expresion regular que abarque varias lineas (por
# ejemplo el bloque de indice entre centinelas) nunca encuentra nada y el
# resultado es un falso negativo.
function CurlText($token, $url) {
    $lines = curl.exe -s $url -H "Authorization: Bearer $token"
    return ($lines -join "`n")
}

Write-Host "Verificando las condiciones contra $API"
WaitForAPI

$ana  = Login 'ana@demo.local'  'Demo12345'
$beto = Login 'beto@demo.local' 'Demo12345'
Pass 'autenticacion de los dos usuarios de prueba'

# --- C1: asincronia efectiva ------------------------------------------------
Section 'C1' 'Asincronia efectiva'

$large = Join-Path $root 'testdata/05-lento-grande.md'
$c1file = if (Test-Path $large) { 'testdata/05-lento-grande.md' } else { 'testdata/02-capitulos.md' }
if ($c1file -ne 'testdata/05-lento-grande.md') {
    Write-Host '  (aviso) 05-lento-grande.md no existe; genere con: go run ./cmd/tools gen-large' -ForegroundColor Yellow
}

$path = Join-Path $root $c1file
$elapsed = Measure-Command {
    $script:c1 = curl.exe -s -X POST "$API/documents" -H "Authorization: Bearer $ana" -F "file=@$path" | ConvertFrom-Json
}
$ms = [int]$elapsed.TotalMilliseconds

if ($c1.status -eq 'queued' -and $c1.job_id) {
    Pass "la API devolvio 202 con job_id y estado 'queued' en $ms ms sin esperar la conversion"
} else {
    Fail "la respuesta de carga no fue un trabajo encolado: $($c1 | ConvertTo-Json -Compress)"
}

# El cliente ya "cerro la conexion": el trabajo debe seguir por su cuenta.
$c1job = WaitJob $ana $c1.job_id
if ($c1job.status -eq 'succeeded') {
    Pass "el trabajo prosiguio sin el cliente y termino en '$($c1job.status)'"
} else {
    Fail "el trabajo termino en '$($c1job.status)' en lugar de 'succeeded'"
}

# --- C2: documento breve ----------------------------------------------------
Section 'C2' 'Documento breve sin divisiones'

$c2 = Upload $ana 'testdata/01-breve.md'
$c2job = WaitJob $ana $c2.job_id

if ($c2job.status -ne 'succeeded') {
    Fail "el trabajo termino en '$($c2job.status)'"
} else {
    $bundle = curl.exe -s "$API/bundles/$($c2job.bundle_id)" -H "Authorization: Bearer $ana" | ConvertFrom-Json
    $paths = @($bundle.files | ForEach-Object { $_.path })

    foreach ($required in @('index.md','log.md','documento.md')) {
        if ($paths -contains $required) { Pass "el bundle contiene $required" }
        else { Fail "falta $required (contiene: $($paths -join ', '))" }
    }

    $warnings = @($c2job.validation_report.findings | Where-Object { $_.axis -eq 'platform' -and $_.severity -eq 'warning' })
    if ($warnings.Count -eq 0) {
        Pass 'cero advertencias: una unidad unica no genera avisos'
    } else {
        Fail "se emitieron $($warnings.Count) advertencias: $($warnings.code -join ', ')"
    }

    if ($c2job.result_class -eq 'valid') { Pass "veredicto 'valid'" }
    else { Fail "veredicto '$($c2job.result_class)'" }
}

# --- C3: documento estructurado --------------------------------------------
Section 'C3' 'Documento estructurado con orden preservado'

$c3 = Upload $ana 'testdata/02-capitulos.md'
$c3job = WaitJob $ana $c3.job_id

if ($c3job.status -ne 'succeeded') {
    Fail "el trabajo termino en '$($c3job.status)'"
} else {
    $bundle = curl.exe -s "$API/bundles/$($c3job.bundle_id)" -H "Authorization: Bearer $ana" | ConvertFrom-Json
    $concepts = @($bundle.files | Where-Object { $_.path -ne 'index.md' -and $_.path -ne 'log.md' -and $_.path -notlike 'assets/*' })

    if ($concepts.Count -ge 4) { Pass "$($concepts.Count) conceptos, uno por unidad" }
    else { Fail "solo $($concepts.Count) conceptos" }

    # El indice debe enlazar los conceptos en orden dentro del bloque delimitado.
    $index = CurlText $ana "$API/bundles/$($c3job.bundle_id)/files/index.md"
    $toc = ''
    if ($index -match '(?s)<!-- okf:toc -->(.*?)<!-- /okf:toc -->') { $toc = $Matches[1] }

    if (-not $toc) {
        Fail 'index.md no contiene el bloque de indice delimitado'
    } else {
        $ordered = $true
        $last = -1
        foreach ($c in ($concepts | Sort-Object seq)) {
            $pos = $toc.IndexOf("./$($c.path)")
            if ($pos -lt 0) { Fail "el indice no enlaza $($c.path)"; $ordered = $false; break }
            if ($pos -lt $last) { $ordered = $false }
            $last = $pos
        }
        if ($ordered) { Pass 'los enlaces del indice siguen el orden del documento de origen' }
        else { Fail 'los enlaces del indice no estan en orden' }
    }
}

# --- C4: bundle incompleto --------------------------------------------------
Section 'C4' 'Bundle que no supera la validacion'

$c4 = Upload $ana 'testdata/04-invalido.txt'
$c4job = WaitJob $ana $c4.job_id

if ($c4job.status -eq 'invalid') { Pass "el trabajo termino en 'invalid'" }
else { Fail "el trabajo termino en '$($c4job.status)' y se esperaba 'invalid'" }

if (-not $c4job.bundle_id) { Pass 'no se publico ningun bundle' }
else { Fail "se publico un bundle ($($c4job.bundle_id)) pese al veredicto invalido" }

$errs = @($c4job.validation_report.findings | Where-Object { $_.axis -eq 'platform' -and $_.severity -eq 'error' })
if ($errs.Count -gt 0) { Pass "$($errs.Count) hallazgo(s) de error visibles: $($errs.code -join ', ')" }
else { Fail 'no hay hallazgos que expliquen el rechazo' }

# Aunque se conozca el identificador del trabajo, no hay descarga posible.
#
# La peticion tiene que ser POST y se exige 404 exactamente. Antes se hacia un
# GET y se aceptaba tambien un 405: como esa ruta solo admite POST, el 405 lo
# devolvia el router sin llegar a mirar el bundle, con lo que la comprobacion
# pasaba sin haber verificado nada. Un test que aprueba por el motivo equivocado
# es peor que uno que falla.
$code = PostStatusOf $ana "$API/bundles/$($c4.job_id)/download-tickets"
if ($code -eq '404') { Pass 'no se puede emitir ticket de descarga para un bundle no publicado (404)' }
else { Fail "el ticket de descarga respondio HTTP $code y se esperaba 404" }

# --- C5: aislamiento --------------------------------------------------------
Section 'C5' 'Aislamiento por propietario'

$victimJob    = $c2job.id
$victimBundle = $c2job.bundle_id
$inventado    = '00000000-0000-0000-0000-000000000000'

$codeJob    = StatusOf $beto "$API/jobs/$victimJob"
$codeBundle = StatusOf $beto "$API/bundles/$victimBundle"
$codeFake   = StatusOf $beto "$API/bundles/$inventado"
$codeFile   = StatusOf $beto "$API/bundles/$victimBundle/files/index.md"

# El documento ORIGINAL es otra ruta que devuelve contenido a partir de un
# identificador, asi que tambien tiene que denegarse. Se comprueba aqui y no solo
# en el visor porque cada endpoint nuevo de este tipo es una ocasion de olvidar el
# filtro por propietario.
$codeOriginal = StatusOf $beto "$API/documents/$($c2job.document_id)/content"

if ($codeJob -eq '404')    { Pass "el trabajo ajeno responde 404 (no 403)" } else { Fail "el trabajo ajeno responde $codeJob" }
if ($codeBundle -eq '404') { Pass "el bundle ajeno responde 404" }           else { Fail "el bundle ajeno responde $codeBundle" }
if ($codeFile -eq '404')   { Pass "el fichero del bundle ajeno responde 404" } else { Fail "el fichero ajeno responde $codeFile" }
if ($codeOriginal -eq '404') { Pass "el documento original ajeno responde 404" } else { Fail "el documento original ajeno responde $codeOriginal" }

if ($codeBundle -eq $codeFake) {
    Pass "el recurso ajeno y uno inexistente son indistinguibles (ambos $codeBundle): no se revela su existencia"
} else {
    Fail "ajeno=$codeBundle e inexistente=$codeFake difieren: se filtra la existencia del recurso"
}

$codeNoAuth = curl.exe -s -o NUL -w "%{http_code}" "$API/jobs/$victimJob"
if ($codeNoAuth -eq '401') { Pass 'sin token responde 401' } else { Fail "sin token responde $codeNoAuth" }

# --- C6: ausencia de duplicados --------------------------------------------
Section 'C6' 'Ausencia de duplicados ante reentrega'

$replay = curl.exe -s -o NUL -w "%{http_code}" -X POST "$API/jobs/$victimJob/replay?times=3" -H "Authorization: Bearer $ana"

if ($replay -eq '404') {
    # `docker compose up` no acepta -e; la variable se toma del entorno del
    # shell, que es lo que interpola ${DEV_TOOLS:-false} en el compose.
    Write-Host '  (aviso) la herramienta de reinyeccion esta desactivada (DEV_TOOLS=false).' -ForegroundColor Yellow
    Write-Host '          Para comprobar C6:' -ForegroundColor Yellow
    Write-Host '            $env:DEV_TOOLS="true"; docker compose up -d api' -ForegroundColor Yellow
    Write-Host '            .\scripts\verify-conditions.ps1' -ForegroundColor Yellow
    Write-Host '            $env:DEV_TOOLS="false"; docker compose up -d api   # dejarlo desactivado' -ForegroundColor Yellow
    Fail 'C6 no se pudo comprobar porque la reinyeccion esta desactivada'
} elseif ($replay -ne '202') {
    Fail "la reinyeccion respondio HTTP $replay"
} else {
    Pass 'se reinyectaron 3 copias del mismo mensaje'
    Start-Sleep -Seconds 6

    $after = curl.exe -s "$API/jobs/$victimJob" -H "Authorization: Bearer $ana" | ConvertFrom-Json

    if ($after.bundle_id -eq $victimBundle) {
        Pass 'el trabajo sigue apuntando al mismo unico bundle'
    } else {
        Fail "el bundle cambio: antes $victimBundle, ahora $($after.bundle_id)"
    }

    $dups = @($after.events | Where-Object { $_.type -eq 'duplicate_delivery_ignored' })
    if ($dups.Count -ge 1) {
        Pass "$($dups.Count) entrega(s) duplicada(s) registradas como descartadas en la traza"
    } else {
        Fail 'la traza no registra ninguna entrega duplicada descartada'
    }

    # Comprobacion directa en la base de datos: un solo bundle para el trabajo.
    $count = docker compose exec -T postgres psql -U okf -d okf -t -A -c `
        "select count(*) from bundles where job_id = '$victimJob';" 2>$null
    if ($count) {
        $count = $count.Trim()
        if ($count -eq '1') { Pass "la base de datos tiene exactamente 1 bundle para el trabajo" }
        else { Fail "la base de datos tiene $count bundles para el trabajo" }
    }
}

# --- resumen ----------------------------------------------------------------
Write-Host ''
if ($fail -eq 0) {
    Write-Host 'Las seis condiciones verificables pasan.' -ForegroundColor Green
    exit 0
} else {
    Write-Host "$fail comprobacion(es) han fallado." -ForegroundColor Red
    exit 1
}
