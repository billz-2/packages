package userclient

import "errors"

var (
	// ErrUserNotFound - пользователя нет: User Service ответил 404 либо id не UUID.
	// Отличим от транспортных ошибок через errors.Is.
	ErrUserNotFound = errors.New("userclient: user not found")

	// ErrEmptyUserServiceURL - в Config не задан UserServiceURL.
	ErrEmptyUserServiceURL = errors.New("userclient: UserServiceURL is required")

	// ErrNilLogger - в Config не задан Logger.
	ErrNilLogger = errors.New("userclient: Logger is required")
)
