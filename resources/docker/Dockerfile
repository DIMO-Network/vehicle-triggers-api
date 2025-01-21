FROM golang:1.23 AS build

RUN useradd -u 10001 dimo

WORKDIR /go/src/github.com/DIMO-Network/vehicle-events-api/
COPY . /go/src/github.com/DIMO-Network/vehicle-events-api/

ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOFLAGS=-mod=vendor

RUN ls
RUN make install

FROM busybox AS package

LABEL maintainer="DIMO <hello@dimo.zone>"

WORKDIR /

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /etc/passwd /etc/passwd
COPY --from=build /go/src/github.com/DIMO-Network/vehicle-events-api/internal/db/migrations ./internal/db/migrations
## Makefile install does build into /bin directory
COPY --from=build /go/src/github.com/DIMO-Network/vehicle-events-api/target/bin/vehicle-events-api .

USER dimo

EXPOSE 8080
EXPOSE 8888

CMD /vehicle-events-api
