FROM golang:1.26 AS build
WORKDIR /src
COPY services/disaggproxy ./services/disaggproxy
WORKDIR /src/services/disaggproxy
RUN CGO_ENABLED=0 go build -o /disaggproxy ./cmd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /disaggproxy /disaggproxy
ENTRYPOINT ["/disaggproxy"]
