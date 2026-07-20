FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download || go mod download || go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/lastping-mcp-server ./cmd/lastping-mcp-server

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/lastping-mcp-server /lastping-mcp-server
ENTRYPOINT ["/lastping-mcp-server"]
