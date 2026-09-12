// Package web sirve la interfaz web estática de la aplicación.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

// assets contiene la interfaz completa para que el binario pueda servirla sin
// depender de archivos presentes en el contenedor en tiempo de ejecución.
//
//go:embed assets/*
var assets embed.FS

// Mount registra las rutas de la interfaz web sobre mux.
func Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		content, err := assets.ReadFile("assets/index.html")
		if err != nil {
			http.Error(w, "no se pudo cargar la interfaz", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(content)
	})

	assetFS, err := fs.Sub(assets, "assets")
	if err != nil {
		panic("subdirectorio de assets inválido: " + err.Error())
	}
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(assetFS))))
}
