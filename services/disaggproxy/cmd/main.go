package main

import (
	"flag"
	"log"
	"net/http"
	"strings"

	"kk-infra/services/disaggproxy/internal/proxy"
)

func main() {
	listen := flag.String("listen", ":8080", "HTTP listen address")
	prefill := flag.String("prefill-endpoints", "", "comma-separated vLLM prefill endpoints")
	decode := flag.String("decode-endpoints", "", "comma-separated vLLM decode endpoints")
	flag.Parse()
	handler, err := proxy.New(split(*prefill), split(*decode), nil)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("disaggregated proxy listening on %s", *listen)
	log.Fatal(http.ListenAndServe(*listen, handler))
}

func split(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
