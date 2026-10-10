package linkedin

import "net/http"

func HTTPClient(c *Connector) *http.Client { return c.httpClient() }
