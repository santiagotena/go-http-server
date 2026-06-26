package main

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/santiagotena/go-http-server/internal/auth"
)

func (cfg *apiConfig) handlerChirpsDelete(w http.ResponseWriter, r *http.Request) {
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}
	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	chirpIDString := r.PathValue("chirpID")
	chirpID, err := uuid.Parse(chirpIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid UUID", err)
		return
	}
	chirp, err := cfg.database.GetChirp(r.Context(), chirpID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Chirp does not exist", err)
		return
	}

	if userID != chirp.UserID {
		respondWithError(w, http.StatusForbidden, "JWT mismatch", err)
		return
	}

	err = cfg.database.DeleteChirp(r.Context(), chirpID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Chirp was not deleted", err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
