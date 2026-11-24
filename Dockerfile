# ── Build stage ───────────────────────────────────────────────
FROM golang:1.26 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/mongo-readiness ./cmd/mongo-readiness

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/phone-email-index ./cmd/phone-email-index

# ── Run stage ─────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12
COPY --from=build /bin/server /server
COPY --from=build /bin/mongo-readiness /mongo-readiness
COPY --from=build /bin/phone-email-index /phone-email-index
EXPOSE 8080
ENTRYPOINT ["/server"]
