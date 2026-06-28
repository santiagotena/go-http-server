package main

import (
	"net/http"
	"sort"

	"github.com/google/uuid"
	"github.com/santiagotena/go-http-server/internal/database"
)

func (cfg *apiConfig) handlerChirpsGet(w http.ResponseWriter, r *http.Request) {
	chirpIDString := r.PathValue("chirpID")
	chirpID, err := uuid.Parse(chirpIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid UUID", err)
		return
	}

	chirp, err := cfg.database.GetChirp(r.Context(), chirpID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "No such chirp found", err)
		return
	}

	respondWithJSON(w, http.StatusOK, Chirp{
		ID:        chirp.ID,
		CreatedAt: chirp.CreatedAt,
		UpdatedAt: chirp.UpdatedAt,
		UserID:    chirp.UserID,
		Body:      chirp.Body,
	})
}

func (cfg *apiConfig) handlerChirpsRetrieve(w http.ResponseWriter, r *http.Request) {
	authorIDString := r.URL.Query().Get("author_id")
	var chirps []database.Chirp
	var err error

	if authorIDString == "" {
		chirps, err = cfg.database.GetAllChirps(r.Context())
		if err != nil {
			respondWithError(w, http.StatusInternalServerError, "Something went wrong while getting chirps", err)
			return
		}
	} else {
		var authorID uuid.UUID
		authorID, err = uuid.Parse(authorIDString)
		if err != nil {
			respondWithError(w, http.StatusBadRequest, "Invalid UUID", err)
			return
		}
		chirps, err = cfg.database.GetChirpByUserId(r.Context(), authorID)
		if err != nil {
			respondWithError(w, http.StatusNotFound, "Could not get chirps from database", err)
			return
		}
	}

	var response []Chirp
	for _, chirp := range chirps {
		chirp := Chirp{
			ID:        chirp.ID,
			CreatedAt: chirp.CreatedAt,
			UpdatedAt: chirp.UpdatedAt,
			Body:      chirp.Body,
			UserID:    chirp.UserID,
		}
		response = append(response, chirp)
	}

	sortParameter := r.URL.Query().Get("sort")
	if sortParameter == "" || sortParameter == "asc" {
		sort.Slice(response, func(i, j int) bool {
			return response[i].CreatedAt.Before(response[j].CreatedAt)
		})
	} else if sortParameter == "desc" {
		sort.Slice(response, func(i, j int) bool {
			return response[i].CreatedAt.After(response[j].UpdatedAt)
		})
	}

	respondWithJSON(w, http.StatusOK, response)
}
