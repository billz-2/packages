package amocrm

type LeadsResponse struct {
	Page     int   `json:"_page"`
	Links    Links `json:"_links"`
	Embedded struct {
		Leads []Lead `json:"leads"`
	} `json:"_embedded"`
}

type Links struct {
	Self  Link `json:"self"`
	Next  Link `json:"next"`
	First Link `json:"first"`
	Prev  Link `json:"prev"`
}

type Link struct {
	Href string `json:"href"`
}

type Lead struct {
	ID                 int         `json:"id"`
	Name               string      `json:"name"`
	Price              float64     `json:"price"`
	ResponsibleUserID  int         `json:"responsible_user_id"`
	GroupID            int         `json:"group_id"`
	StatusID           int         `json:"status_id"`
	PipelineID         int         `json:"pipeline_id"`
	LossReasonID       interface{} `json:"loss_reason_id"` // null = interface{}
	SourceID           interface{} `json:"source_id"`
	CreatedBy          int         `json:"created_by"`
	UpdatedBy          int         `json:"updated_by"`
	CreatedAt          int64       `json:"created_at"`
	UpdatedAt          int64       `json:"updated_at"`
	ClosedAt           int64       `json:"closed_at"`
	ClosestTaskAt      interface{} `json:"closest_task_at"`
	IsDeleted          bool        `json:"is_deleted"`
	CustomFieldsValues interface{} `json:"custom_fields_values"` // optional: can be refined if needed
	Score              interface{} `json:"score"`
	AccountID          int         `json:"account_id"`
	Links              struct {
		Self Link `json:"self"`
	} `json:"_links"`
	Embedded struct {
		Tags      []interface{} `json:"tags"`
		Companies []interface{} `json:"companies"`
	} `json:"_embedded"`
}

type Tag struct {
	ID   *int    `json:"id,omitempty"`
	Name *string `json:"name,omitempty"`
}

type Embedded struct {
	Tags []*Tag `json:"tags,omitempty"`
}

type LeadUpdate struct {
	ID             int       `json:"id"`
	Name           *string   `json:"name,omitempty"`
	Price          *int      `json:"price,omitempty"`
	StatusID       *int      `json:"status_id,omitempty"`
	PipelineID     *int      `json:"pipeline_id,omitempty"`
	ClosedAt       *int64    `json:"closed_at,omitempty"`
	CreatedBy      *int      `json:"created_by,omitempty"`
	UpdatedBy      *int      `json:"updated_by,omitempty"`
	CreatedAt      *int64    `json:"created_at,omitempty"`
	UpdatedAt      *int64    `json:"updated_at,omitempty"`
	LossReasonID   *int      `json:"loss_reason_id,omitempty"`
	ResponsibleUID *int      `json:"responsible_user_id,omitempty"`
	CustomFields   any       `json:"custom_fields_values,omitempty"` // You can define a struct for custom fields if needed
	TagsToAdd      []*Tag    `json:"tags_to_add,omitempty"`
	TagsToDelete   []*Tag    `json:"tags_to_delete,omitempty"`
	Embedded       *Embedded `json:"_embedded,omitempty"`
}
