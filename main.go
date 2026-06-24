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
	"github.com/santiagotena/go-http-server/internal/auth"
	"github.com/santiagotena/go-http-server/internal/database"
)

import _ "github.com/lib/pq"

func main() {
	const filepathRoot = "."
	const port = "8080"

	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}
	platform := os.Getenv("PLATFORM")
	if platform == "" {
		log.Fatal("PLATFORM environment variable not set")
	}
	dbURL := os.Getenv("DB_URL")
	if dbURL == "" {
		log.Fatal("DB_URL environment variable not set")
	}
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		log.Fatal("JWT_SECRET environment variable not set")
	}

	dbConn, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("error connecting to the database: %s", err)
	}
	dbQueries := database.New(dbConn)

	apiCfg := &apiConfig{
		database:  dbQueries,
		platform:  platform,
		jwtSecret: jwtSecret,
	}

	mux := http.NewServeMux()

	fileServer := http.FileServer(http.Dir(filepathRoot))

	mux.Handle(
		"/app/",
		apiCfg.middlewareMetricsInc(
			middlewareLog(
				http.StripPrefix("/app", fileServer),
			),
		),
	)

	mux.HandleFunc("POST /api/users", apiCfg.createUserHandler)
	mux.HandleFunc("POST /api/login", apiCfg.loginUserHandler)
	mux.HandleFunc("GET /api/chirps", apiCfg.getChirps)
	mux.HandleFunc("GET /api/chirps/{chirpID}", apiCfg.getChirp)
	mux.HandleFunc("POST /api/chirps", apiCfg.validateChirp)
	mux.HandleFunc("GET /admin/metrics", apiCfg.readMetricsHandler)
	mux.HandleFunc("POST /admin/reset", apiCfg.resetBackEndHandler)
	mux.HandleFunc("GET /api/healthz", readinessHandler)

	server := &http.Server{
		Handler: mux,
		Addr:    ":" + port,
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
	jwtSecret      string
}

type User struct {
	ID        uuid.UUID `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Email     string    `json:"email"`
}

func (cfg *apiConfig) middlewareMetricsInc(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg.fileserverHits.Add(1)
		next.ServeHTTP(w, r)
	})
}

func (cfg *apiConfig) createUserHandler(w http.ResponseWriter, r *http.Request) {
	type User struct {
		Password string `json:"password"`
		Email    string `json:"email"`
	}

	decoder := json.NewDecoder(r.Body)
	user := User{}
	err := decoder.Decode(&user)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Something went wrong")
		return
	}

	type Payload struct {
		ID        uuid.UUID `json:"id"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
		Email     string    `json:"email"`
	}

	email := user.Email
	hashedPassword, err := auth.HashPassword(user.Password)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Problem hashing password")
	}

	newUser, err := cfg.database.CreateUser(r.Context(), database.CreateUserParams{Email: email, HashedPassword: hashedPassword})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Could not create user")
		return
	}

	payload := &Payload{ID: newUser.ID, CreatedAt: newUser.CreatedAt, UpdatedAt: newUser.UpdatedAt, Email: newUser.Email}
	respondWithJSON(w, http.StatusCreated, payload)
}

func (cfg *apiConfig) loginUserHandler(w http.ResponseWriter, r *http.Request) {
	type parameters struct {
		Password         string `json:"password"`
		Email            string `json:"email"`
		ExpiresInSeconds int    `json:"expires_in_seconds"`
	}
	type response struct {
		User
		Token        string `json:"token"`
		RefreshToken string `json:"refresh_token"`
	}

	decoder := json.NewDecoder(r.Body)
	params := parameters{}
	err := decoder.Decode(&params)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't decode parameters")
		return
	}

	user, err := cfg.database.GetUserByEmail(r.Context(), params.Email)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Incorrect email or password")
		return
	}

	match, err := auth.CheckPasswordHash(params.Password, user.HashedPassword)
	if err != nil || !match {
		respondWithError(w, http.StatusUnauthorized, "Incorrect email or password")
		return
	}

	expirationTime := time.Hour
	if params.ExpiresInSeconds > 0 && params.ExpiresInSeconds <= 3600 {
		expirationTime = time.Duration(params.ExpiresInSeconds) * time.Second
	}

	accessToken, err := auth.MakeJWT(
		user.ID,
		cfg.jwtSecret,
		expirationTime,
	)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create access JWT")
		return
	}

	respondWithJSON(w, http.StatusOK, response{
		User: User{
			ID:        user.ID,
			CreatedAt: user.CreatedAt,
			UpdatedAt: user.UpdatedAt,
			Email:     user.Email,
		},
		Token: accessToken,
	})
}

func (cfg *apiConfig) getChirps(w http.ResponseWriter, r *http.Request) {
	chirps, err := cfg.database.GetAllChirps(r.Context())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Something went wrong while getting chirps")
		return
	}

	type Chirp struct {
		ID        uuid.UUID `json:"id"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
		Body      string    `json:"body"`
		UserID    uuid.UUID `json:"user_id"`
	}

	var payload []Chirp
	for _, chirp := range chirps {
		chirp := Chirp{
			ID:        chirp.ID,
			CreatedAt: chirp.CreatedAt,
			UpdatedAt: chirp.UpdatedAt,
			Body:      chirp.Body,
			UserID:    chirp.UserID,
		}
		payload = append(payload, chirp)
	}
	respondWithJSON(w, http.StatusOK, payload)
}

func (cfg *apiConfig) getChirp(w http.ResponseWriter, r *http.Request) {
	chirpIDString := r.PathValue("chirpID")
	chirpID, err := uuid.Parse(chirpIDString)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Invalid UUID")
		return
	}

	chirp, err := cfg.database.GetChirp(r.Context(), chirpID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "No such chirp found")
		return
	}

	type Payload struct {
		ID        uuid.UUID `json:"id"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
		Body      string    `json:"body"`
		UserID    uuid.UUID `json:"user_id"`
	}

	payload := &Payload{
		ID:        chirp.ID,
		CreatedAt: chirp.CreatedAt,
		UpdatedAt: chirp.UpdatedAt,
		Body:      chirp.Body,
		UserID:    chirp.UserID,
	}

	respondWithJSON(w, http.StatusOK, payload)
}

func (cfg *apiConfig) validateChirp(w http.ResponseWriter, r *http.Request) {
	type Chirp struct {
		Body string `json:"body"`
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT")
		return
	}
	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT")
		return
	}

	decoder := json.NewDecoder(r.Body)
	chirp := Chirp{}
	err = decoder.Decode(&chirp)
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
		ID        uuid.UUID `json:"id"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
		Body      string    `json:"body"`
		UserID    uuid.UUID `json:"user_id"`
	}

	newChirp, err := cfg.database.CreateChirp(r.Context(), database.CreateChirpParams{Body: cleanedBody, UserID: userID})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Something went wrong with chirp creation")
		return
	}

	payload := &Payload{
		ID:        newChirp.ID,
		CreatedAt: newChirp.CreatedAt,
		UpdatedAt: newChirp.UpdatedAt,
		Body:      cleanedBody,
		UserID:    userID,
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
