CREATE TABLE IF NOT EXISTS items (
    id bigint PRIMARY KEY,
    version bigint NOT NULL CHECK (version > 0),
    payload text NOT NULL
);
CREATE TABLE IF NOT EXISTS item_history (
    id bigint NOT NULL REFERENCES items (id),
    version bigint NOT NULL CHECK (version > 0),
    PRIMARY KEY (id, version)
);
