package corvallisbus

import (
	"github.com/mjibson/appstats"
	"net/http"
)

const baseURL = "http://www.corvallistransit.com/"

func init() {
	http.Handle("/routes", appstats.NewHandler(Routes))
	http.Handle("/stops", appstats.NewHandler(Stops))
	http.Handle("/arrivals", appstats.NewHandler(Arrivals))
}
