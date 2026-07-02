package models

import "time"

type Job struct {
	ID        string        `json:"id"`
	Language  string        `json:"language"`
	Code      string        `json:"code"`
	Stdin     string        `json:"stdin"`
	Timeout   time.Duration `json:"timeout"`
}
