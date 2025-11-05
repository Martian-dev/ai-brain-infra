package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Martian-dev/ai-brain-infra/internal/store"
	"golang.org/x/crypto/bcrypt"
)

type AuthService struct {
	basePath string
}

func NewAuthService(basePath string) *AuthService {
	return &AuthService{basePath: basePath}
}

func (s *AuthService) CreateUser(username, password string) (*User, error) {
	// Check if user directory already exists
	if _, err := os.Stat(filepath.Join(s.basePath, username)); !os.IsNotExist(err) {
		return nil, errors.New("username already exists")
	}

	// Create user store
	userStore, err := store.NewUserStore(s.basePath, username)
	if err != nil {
		return nil, fmt.Errorf("failed to create user store: %w", err)
	}
	defer userStore.Close()

	// Hash password and store auth info
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	err = userStore.StoreAuth(username, string(hashedPassword))
	if err != nil {
		// Cleanup user directory if auth storage fails
		os.RemoveAll(filepath.Join(s.basePath, username))
		return nil, err
	}

	user := &User{
		Username:  username,
		Password:  string(hashedPassword),
		CreatedAt: time.Now(),
	}

	return user, nil
}

func (s *AuthService) ValidateUser(username, password string) (*User, error) {
	// Try to open the user's store directly by username
	userStore, err := store.NewUserStore(s.basePath, username)
	if err != nil {
		return nil, errors.New("invalid username or password")
	}
	defer userStore.Close()

	auth, err := userStore.GetAuth(username)
	if err != nil || auth == nil {
		return nil, errors.New("invalid username or password")
	}

	// Validate password
	err = bcrypt.CompareHashAndPassword([]byte(auth.Password), []byte(password))
	if err != nil {
		return nil, errors.New("invalid username or password")
	}

	return &User{
		Username:  auth.Username,
		Password:  auth.Password,
		CreatedAt: auth.CreatedAt,
	}, nil
}