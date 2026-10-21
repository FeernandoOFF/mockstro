package wiremock

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// AddMapping registers one stub on the running server and returns the id WireMock gave it.
// Unlike Replace it leaves every other mapping and the request journal alone.
func (s *Server) AddMapping(mapping any) (string, error) {
	if err := s.AssertOwn(); err != nil {
		return "", err
	}
	b, err := s.do(http.MethodPost, "/__admin/mappings", mapping)
	if err != nil {
		return "", fmt.Errorf("add WireMock mapping: %w", err)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &created); err != nil || created.ID == "" {
		return "", fmt.Errorf("WireMock did not return a mapping id")
	}
	return created.ID, nil
}

// RemoveMapping deletes one stub by id.
func (s *Server) RemoveMapping(id string) error {
	if err := s.AssertOwn(); err != nil {
		return err
	}
	if _, err := s.do(http.MethodDelete, "/__admin/mappings/"+url.PathEscape(id), nil); err != nil {
		return fmt.Errorf("remove WireMock mapping: %w", err)
	}
	return nil
}

// Request returns one journal entry, with the request and response in full.
func (s *Server) Request(id string) (*LoggedRequest, error) {
	b, err := s.do(http.MethodGet, "/__admin/requests/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, fmt.Errorf("read WireMock request %s: %w", id, err)
	}
	var r LoggedRequest
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ReplaceMapping overwrites the stub with the given id, leaving every other mapping and the
// request journal alone.
func (s *Server) ReplaceMapping(id string, mapping any) error {
	if err := s.AssertOwn(); err != nil {
		return err
	}
	if _, err := s.do(http.MethodPut, "/__admin/mappings/"+url.PathEscape(id), mapping); err != nil {
		return fmt.Errorf("replace WireMock mapping: %w", err)
	}
	return nil
}
