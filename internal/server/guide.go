package server

import "net/http"

// Temas de la guía, en el orden de sus pestañas.
var guideTopics = []struct{ Key, Label string }{
	{"enlaces", "Archivos enlazados"},
	{"carpetas", "Carpetas"},
	{"problemas", "Si algo no se ve"},
}

type guideData struct {
	Topic string
	Tabs  []tabData
}

// webGuide es el tutorial dentro de la web, para no depender de un PDF que se
// queda viejo. Cada tema es una pestaña: /guia?tema=carpetas.
func (s *Server) webGuide(w http.ResponseWriter, r *http.Request) {
	topic := r.URL.Query().Get("tema")
	data := guideData{Topic: guideTopics[0].Key}
	for _, t := range guideTopics {
		if t.Key == topic {
			data.Topic = topic
		}
	}
	for _, t := range guideTopics {
		data.Tabs = append(data.Tabs, tabData{Label: t.Label, Href: "/guia?tema=" + t.Key, Selected: t.Key == data.Topic})
	}
	s.render(w, r, http.StatusOK, "guide", pageData{Title: "Guía", Active: "guide", Data: data})
}
