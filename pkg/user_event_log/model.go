package usereventlog

type EventLogModel struct {
	ID               int64       `json:"id"`
	EventID          string      `json:"event_id"`           // event union id
	EventActionType  string      `json:"event_action_type"`  // 'create', 'update', 'delete'
	EventData        interface{} `json:"event_data"`         // dynamic obj
	EventSource      string      `json:"event_source"`       // topic: catalog, inv, order, fin
	ParentObjectName string      `json:"parent_object_name"` // 'product_id' for product child entities, 'order_id' for order childs
	ParentObjectID   string      `json:"parent_object_id"`   // product_id, order_id, supplier_id
	ObjectID         string      `json:"object_id"`          // entity_id (product_id, category_id, order_id)
	ObjectType       string      `json:"object_type"`        //'product', 'product_price', 'product_detail','order','product_attribute'
	CompanyID        string      `json:"company_id"`
	UserID           string      `json:"user_id"`
	SessionID        string      `json:"session_id"`
	CreatedAt        string      `json:"created_at"`
	RetryCount       int64       `json:"retry_count"` // 100 max
	ErrorMsg         string      `json:"error_message"`
	Deleted          bool        `json:"deleted"`
	Saved            bool        `json:"saved"`
	Status           int64       `json:"status"`
}

type EventLogReq struct {
	CompanyID        string   `json:"company_id"`
	UserID           string   `json:"user_id"`
	SessionID        string   `json:"session_id"`
	EventID          string   `json:"event_id"`           // event union id
	EventActionType  string   `json:"event_action_type"`  // 'create', 'update', 'delete'
	EventSource      string   `json:"event_source"`       // topic: catalog, inv, order, fin
	ParentObjectName string   `json:"parent_object_name"` // 'product_id' for product child entities, 'order_id' for order childs
	ParentObjectID   string   `json:"parent_object_id"`   // product_id, order_id, supplier_id
	ObjectID         string   `json:"object_id"`          // entity_id (product_id, category_id, order_id)
	ObjectType       string   `json:"object_type"`        //'product', 'product_price', 'product_detail','order','product_attribute'
	IDs              []string `json:"ids"`
}

type EventLogsResp struct {
	EventLogs []EventLogModel `json:"data"`
}
