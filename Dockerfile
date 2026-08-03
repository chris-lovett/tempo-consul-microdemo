# syntax=docker/dockerfile:1.7
FROM golang:1.24-alpine AS build
ARG SERVICE
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN test -n "${SERVICE}"
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/${SERVICE}

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/service /service
ENTRYPOINT ["/service"]
