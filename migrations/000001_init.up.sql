CREATE TABLE deliveries (
    seq BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    delivery_id UUID NOT NULL UNIQUE,
    event_id TEXT NOT NULL,
    recipient_user_ids TEXT[] NOT NULL,
    payload BYTEA NOT NULL,
    sent_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX deliveries_event_id_seq_idx ON deliveries (event_id, seq);
