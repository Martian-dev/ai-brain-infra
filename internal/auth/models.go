package auth

import (
	"time"
)

type User struct {
	Username  string    `json:"username"`
	Password  string    `json:"-"` // "-" means this field won't be included in JSON
	CreatedAt time.Time `json:"created_at"`
}