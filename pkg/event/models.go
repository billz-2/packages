package event

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Response struct {
	Topic         string      `json:"topic,omitempty"`
	Slug          string      `json:"slug,omitempty"`
	NoResponse    bool        `json:"no_response"`
	SessionID     string      `json:"session_id,omitempty" swaggerignore:"true"`
	StatusCode    int32       `json:"status_code,omitempty"`
	ID            string      `json:"id,omitempty"`
	Error         Error       `json:"error,omitempty"`
	Data          interface{} `json:"data,omitempty"`
	CorrelationID string      `json:"correlation_id,omitempty"`
	CompanyID     string      `json:"company_id,omitempty"`
	Message       string      `json:"message"`
}

type KafkaConfig struct {
	KafkaUrl        string
	KafkaUserName   string
	KafkaPassword   string
	KafkaCert       string
	ConsumerGroupID string
}
