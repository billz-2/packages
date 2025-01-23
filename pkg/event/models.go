package event

type Response struct {
	Topic         string      `json:"topic,omitempty"`
	Slug          string      `json:"slug"`
	NoResponse    bool        `json:"no_response,omitempty"`
	CompanyID     string      `json:"company_id"`
	SessionID     string      `json:"session_id,omitempty"`
	StatusCode    int32       `json:"status_code,omitempty"`
	ID            string      `json:"id,omitempty"`
	Error         Error       `json:"error,omitempty"`
	Data          interface{} `json:"data,omitempty"`
	CorrelationID string      `json:"correlation_id,omitempty"`
	Message       string      `json:"message"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Config struct {
	BootstrapURL      string `json:"bootstrap_url"`
	Username          string `json:"username,omitempty"`
	Password          string `json:"Password,omitempty"`
	ServerCertificate string `json:"server_certificate,omitempty"`
	ClientCertificate string `json:"client_certificate,omitempty"`
	ClientKey         string `json:"client_key,omitempty"`
	HeartbeatInterval int    `json:"heartbeat_interval,omitempty"`
	SessionTimeout    int    `json:"session_timeout,omitempty"`
	RebalanceTimeout  int    `json:"rebalance_timeout,omitempty"`
	ConsumerGroupID   string `json:"consumer_group_id"`
}
