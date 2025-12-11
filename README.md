# SMTP-to-SendGrid Relay

Relay SMTP ligero que recibe emails vía SMTP (puerto 25) y los reenvía a SendGrid mediante HTTP API (puerto 443).

Diseñado para entornos Kubernetes donde los puertos SMTP salientes (25, 465, 587) están bloqueados (DigitalOcean, GKE, etc.).

## Arquitectura

```
┌─────────────────────────────────────────────────────────────────┐
│                    Kubernetes Cluster                            │
│                                                                  │
│   Keycloak ──SMTP:25──> smtp-relay ──HTTPS:443──> SendGrid API  │
│              (interno)                 (permitido)               │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

## Características

- **Ligero**: Imagen Docker ~15MB (Go + Alpine)
- **Seguro**: Sin autenticación interna (diseñado para cluster)
- **Robusto**: Maneja emails multipart (text/html)
- **Observable**: Logs estructurados con niveles configurables
- **Simple**: Solo necesita `SENDGRID_API_KEY`

## Uso Rápido

### Docker

```bash
docker run -d \
  -p 25:25 \
  -e SENDGRID_API_KEY=SG.xxxxxxxx \
  ghcr.io/themxcode/smtp-relay:latest
```

### Docker Compose

```yaml
version: '3.8'
services:
  smtp-relay:
    image: ghcr.io/themxcode/smtp-relay:latest
    ports:
      - "25:25"
    environment:
      - SENDGRID_API_KEY=${SENDGRID_API_KEY}
      - LOG_LEVEL=info
```

### Kubernetes (Helm)

```bash
helm install smtp-relay ./helm/smtp-relay \
  --namespace conta-prod \
  --set sendgrid.apiKey=SG.xxxxxxxx
```

## Configuración

| Variable | Descripción | Default |
|----------|-------------|---------|
| `SENDGRID_API_KEY` | API Key de SendGrid **(requerido)** | - |
| `SMTP_LISTEN_ADDR` | Dirección de escucha | `:25` |
| `SMTP_DOMAIN` | Dominio del servidor SMTP | `localhost` |
| `LOG_LEVEL` | Nivel de log: debug, info, warn, error | `info` |
| `ALLOWED_SENDERS` | Dominios permitidos (separados por coma) | (todos) |

## Ejemplo: Configurar Keycloak

En Keycloak Admin Console → Realm Settings → Email:

```
Host: smtp-relay.conta-prod.svc.cluster.local
Port: 25
From: noreply@conta-cloud.mx
From Display Name: ContaCloud
Enable SSL: false
Enable StartTLS: false
Authentication: false
```

## Desarrollo Local

### Requisitos

- Go 1.22+
- Docker (opcional)

### Compilar

```bash
go build -o smtp-relay .
```

### Ejecutar

```bash
export SENDGRID_API_KEY=SG.xxxxxxxx
export LOG_LEVEL=debug
./smtp-relay
```

### Probar

```bash
# Enviar email de prueba con swaks
swaks --to test@example.com \
      --from noreply@conta-cloud.mx \
      --server localhost:25 \
      --header "Subject: Test Email" \
      --body "Hello from smtp-relay!"
```

### Build Docker

```bash
docker build -t smtp-relay:local .
docker run -p 25:25 -e SENDGRID_API_KEY=SG.xxx smtp-relay:local
```

## Seguridad

- **Sin autenticación**: Este relay está diseñado para ejecutarse dentro del cluster, donde solo servicios internos pueden acceder al puerto 25.
- **No exponer externamente**: Nunca expongas el puerto 25 fuera del cluster.
- **ALLOWED_SENDERS**: Opcionalmente restringe qué dominios pueden enviar.

## Métricas y Monitoreo

El relay imprime logs estructurados:

```
[INFO] Email sent successfully: from=noreply@conta-cloud.mx to=[user@example.com] subject="Welcome" duration=245ms
```

Para Kubernetes, usa el TCP probe en puerto 25 para health checks.

## Licencia

MIT - ContaCloud / TheMXCode
