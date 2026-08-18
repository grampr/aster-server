FROM golang:1.26.6-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/aster-server ./cmd/server

FROM alpine:3.23

RUN addgroup -S aster && adduser -S -G aster aster
COPY --from=build /out/aster-server /usr/local/bin/aster-server
USER aster
EXPOSE 8080
ENTRYPOINT ["aster-server"]
