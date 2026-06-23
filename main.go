package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/santiagotena/go-http-server/internal/database"
)

import _ "github.com/lib/pq"

func main() {
	err := godotenv.Load()
	if err != nil {
		return
	}
	platform := os.Getenv("PLATFORM")
	dbURL := os.Getenv("DB_URL")
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatal(err)
	}

	dbQueries := database.New(db)

	cfg := &apiConfig{
		database: dbQueries,
		platform: platform,
	}

	mux := http.NewServeMux()

	fileServer := http.FileServer(http.Dir("."))

	mux.Handle(
		"/app/",
		cfg.middlewareMetricsInc(
			middlewareLog(
				http.StripPrefix("/app", fileServer),
			),
		),
	)

	mux.HandleFunc("POST /api/users", cfg.createUserHandler)
	mux.HandleFunc("GET /api/chirps", cfg.getChirps)
	mux.HandleFunc("POST /api/chirps", cfg.validateChirp)
	mux.HandleFunc("GET /admin/metrics", cfg.readMetricsHandler)
	mux.HandleFunc("POST /admin/reset", cfg.resetBackEndHandler)
	mux.HandleFunc("GET /api/healthz", readinessHandler)

	server := &http.Server{
		Handler: mux,
		Addr:    ":8080",
	}

	log.Fatal(server.ListenAndServe())
}

func middlewareLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

type apiConfig struct {
	fileserverHits atomic.Int32
	database       *database.Queries
	platform       string
}

func (cfg *apiConfig) middlewareMetricsInc(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg.fileserverHits.Add(1)
		next.ServeHTTP(w, r)
	})
}

func (cfg *apiConfig) createUserHandler(w http.ResponseWriter, r *http.Request) {
	type User struct {
		Email string `json:"email"`
	}

	decoder := json.NewDecoder(r.Body)
	u := User{}
	err := decoder.Decode(&u)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Something went wrong")
		return
	}

	type Payload struct {
		Id        uuid.UUID `json:"id"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
		Email     string    `json:"email"`
	}

	email := u.Email

	newUser, err := cfg.database.CreateUser(r.Context(), email)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Could not create user")
		return
	}

	payload := &Payload{Id: newUser.ID, CreatedAt: newUser.CreatedAt, UpdatedAt: newUser.UpdatedAt, Email: newUser.Email}
	respondWithJSON(w, http.StatusCreated, payload)
}

func (cfg *apiConfig) getChirps(w http.ResponseWriter, r *http.Request) {
	chirps, err := cfg.database.GetAllChirps(r.Context())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Something went wrong while getting chirps")
		return
	}

	type Chirp struct {
		Id        uuid.UUID `json:"id"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
		Body      string    `json:"body"`
		UserId    uuid.UUID `json:"user_id"`
	}

	var payload []Chirp
	for _, chirp := range chirps {
		chirp := Chirp{
			Id:        chirp.ID,
			CreatedAt: chirp.CreatedAt,
			UpdatedAt: chirp.UpdatedAt,
			Body:      chirp.Body,
			UserId:    chirp.UserID,
		}
		payload = append(payload, chirp)
	}
	respondWithJSON(w, http.StatusOK, payload)
}

func (cfg *apiConfig) validateChirp(w http.ResponseWriter, r *http.Request) {
	type Chirp struct {
		Body   string    `json:"body"`
		UserId uuid.UUID `json:"user_id"`
	}

	decoder := json.NewDecoder(r.Body)
	chirp := Chirp{}
	err := decoder.Decode(&chirp)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Something went wrong")
		return
	}
	if len(chirp.Body) > 140 {
		respondWithError(w, http.StatusBadRequest, "Chirp is too long")
		return
	}

	cleanedBody := censorProfanity(chirp.Body)

	type Payload struct {
		Id        uuid.UUID `json:"id"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
		Body      string    `json:"body"`
		UserId    uuid.UUID `json:"user_id"`
	}

	newChirp, err := cfg.database.CreateChirp(r.Context(), database.CreateChirpParams{Body: cleanedBody, UserID: chirp.UserId})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Something went wrong with chirp creation")
		return
	}

	payload := &Payload{
		Id:        newChirp.ID,
		CreatedAt: newChirp.CreatedAt,
		UpdatedAt: newChirp.UpdatedAt,
		Body:      cleanedBody,
		UserId:    chirp.UserId,
	}
	respondWithJSON(w, http.StatusCreated, payload)
}

func (cfg *apiConfig) readMetricsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	htmlTemplate := `<html>
	<body>
	<h1>Welcome, Chirpy Admin</h1>
	<p>Chirpy has been visited %d times!</p>
	</body>
	</html>`
	message := fmt.Sprintf(htmlTemplate, cfg.fileserverHits.Load())
	_, err := w.Write([]byte(message))
	if err != nil {
		return
	}
}

func (cfg *apiConfig) resetBackEndHandler(w http.ResponseWriter, r *http.Request) {
	if cfg.platform != "dev" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	err := cfg.database.DeleteUser(r.Context())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Could not delete users")
		return
	}
	cfg.fileserverHits.Store(0)
	w.WriteHeader(http.StatusOK)
}

func censorProfanity(chirp string) string {
	profaneWords := map[string]bool{
		"kerfuffle": true,
		"sharbert":  true,
		"fornax":    true,
	}

	words := strings.Split(chirp, " ")

	for i, word := range words {
		if profaneWords[strings.ToLower(word)] {
			words[i] = "****"
		}
	}

	cleanedBody := strings.Join(words, " ")

	return cleanedBody
}

func respondWithError(w http.ResponseWriter, code int, msg string) {
	type ErrorPayload struct {
		Error string `json:"error"`
	}

	respBody := ErrorPayload{
		Error: msg,
	}
	dat, err := json.Marshal(respBody)
	if err != nil {
		log.Printf("Error marshalling JSON: %s", err)
		w.WriteHeader(500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, err = w.Write(dat)
	if err != nil {
		return
	}
}

func respondWithJSON(w http.ResponseWriter, code int, payload interface{}) {
	dat, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Error marshalling JSON: %s", err)
		w.WriteHeader(500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, err = w.Write(dat)
	if err != nil {
		return
	}
}

func readinessHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte("OK"))
	if err != nil {
		return
	}
}
