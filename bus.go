package corvallisbus

import (
	cts "github.com/cvanderschuere/go-connexionz"
	"encoding/json"
	"fmt"
	"net/http"

	"appengine"
)

const baseURL = "http://www.corvallistransit.com/"

func init() {
	http.HandleFunc("/platforms", platforms)
	http.HandleFunc("/routes", routes)
}

func routes(w http.ResponseWriter, r *http.Request) {
	context := appengine.NewContext(r)

	c := cts.New(context, baseURL)

	p := &cts.Platform{
		Tag: r.FormValue("tag"),
	}

	routes, err := c.ETA(p)

	if err != nil {
		return
	}

	//Return as json
	data, errJSON := json.Marshal(routes)
	if errJSON != nil {
		return
	}

	fmt.Fprint(w, string(data))
}

func platforms(w http.ResponseWriter, r *http.Request) {
	context := appengine.NewContext(r)

	//Query for all platforms
	c := cts.New(context, baseURL)
	platforms, err := c.Platforms()

	if err != nil {
		return
	}

	//Return as json
	data, errJSON := json.Marshal(platforms)
	if errJSON != nil {
		return
	}

	fmt.Fprint(w, string(data))
}
