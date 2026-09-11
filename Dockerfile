# --- Etapa de build -----------------------------------------------------
# Compila el binario en un contenedor con el toolchain de Go. Esta etapa no
# forma parte de la imagen final, así que no importa que sea pesada.
FROM golang:1.24-alpine AS builder

WORKDIR /src

# Copiamos primero go.mod/go.sum y descargamos dependencias en una capa
# separada: mientras no cambien esos archivos, Docker reusa esta capa en
# vez de volver a descargar todo en cada build.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 genera un binario estático, necesario para poder correrlo
# sobre la imagen final "scratch" (sin libc).
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/gepa-cli .

# --- Etapa final ----------------------------------------------------------
# Imagen mínima: solo el binario estático, sin shell ni herramientas extra,
# para reducir superficie de ataque y tamaño de imagen.
FROM scratch

# Certificados TLS, por si en el futuro el binario necesita hacer llamadas
# HTTPS salientes.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /out/gepa-cli /usr/local/bin/gepa-cli

EXPOSE 8080

ENTRYPOINT ["gepa-cli"]
# Comando por defecto: levantar la API REST. Se puede pisar para correr
# subcomandos de la CLI, ej: `docker run <imagen> player add "Messi"`.
CMD ["serve", ":8080"]
