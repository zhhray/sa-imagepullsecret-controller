# Build stage
FROM docker-mirrors.alauda.cn/library/golang:1.23.6 as builder
RUN apt-get update

ENV GONOSUMDB="*/*,*.*"
ENV GOPROXY="https://build-nexus.alauda.cn/repository/golang/,direct"

WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o controller ./cmd/controller

# Final stage
FROM build-harbor.alauda.cn/ops/distroless-static:20220806
WORKDIR /
COPY --from=builder /app/controller /controller
ENTRYPOINT ["/controller" ]
