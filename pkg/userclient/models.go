package userclient

// User - зеркало billz_user_service/models.User (models/user.go:9-45).
// Один и тот же JSON отдаётся ручкой GET /v1/user/:id и лежит в ключе Redis "user:<uuid>".
//
// Структуру нельзя урезать до одного Language: пакет общий, следующему потребителю
// понадобится другое поле. Неизвестные поля encoding/json игнорирует, поэтому
// user_service может добавлять свои поля, не ломая потребителей.
type User struct {
	ID                             string `json:"id"`
	ExternalID                     int64  `json:"external_id"`
	CompanyID                      string `json:"company_id"`
	PhoneNumber                    string `json:"phone_number"`
	FirstName                      string `json:"first_name"`
	LastName                       string `json:"last_name"`
	BirthDate                      string `json:"birth_date"`
	Gender                         string `json:"gender"`
	MobileLoginAllowed             *bool  `json:"mobile_login_allowed,omitempty"`
	EnableMaxValidatedDevicesLimit *bool  `json:"enable_max_validated_devices_limit,omitempty"`
	MaxValidatedDevices            *int64 `json:"max_validated_devices,omitempty"`
	StatusID                       string `json:"status_id"`

	IsActive bool `json:"is_active"`

	Image            string        `json:"image"`
	ImageURL         string        `json:"image_url"`
	IsImageAvatar    bool          `json:"is_image_avatar"`
	Language         string        `json:"language"`
	Theme            string        `json:"theme"`
	CurrentCashboxID string        `json:"current_cashbox_id"`
	CurrentShopID    string        `json:"current_shop_id"`
	Cashboxes        []UserCashbox `json:"cashboxes"`
	Shops            []UserShop    `json:"shops"`
	Roles            []UserRole    `json:"roles"`
	CreatedAt        string        `json:"created_at"`
	UpdatedAt        string        `json:"updated_at"`
	DeletedAt        int64         `json:"deleted_at"`
	TelegramID       int64         `json:"telegram_id"`
	PropsUpdated     bool          `json:"props_updated"`

	Type int `json:"type"`
}

// UserCashbox - касса, привязанная к пользователю.
type UserCashbox struct {
	ID        string         `json:"id"`
	CashboxID string         `json:"cashbox_id"`
	Cashbox   map[string]any `json:"cashbox"`
	ShopID    string         `json:"shop_id"`
}

// UserShop - магазин, привязанный к пользователю.
type UserShop struct {
	ID     string         `json:"id"`
	ShopID string         `json:"shop_id"`
	Shop   map[string]any `json:"shop"`
}

// UserRole - роль, привязанная к пользователю.
type UserRole struct {
	ID     string         `json:"id"`
	RoleID string         `json:"role_id"`
	Role   map[string]any `json:"role"`
}
