package corvallisbus

import (
	"gopkg.in/mjibson/v1/appstats"
	"net/http"
)

const baseURL = "http://www.corvallistransit.com/"

func init() {
	http.Handle("/routes", appstats.NewHandler(Routes))
	http.Handle("/stops", appstats.NewHandler(Stops))
	http.Handle("/arrivals", appstats.NewHandler(Arrivals))
}
