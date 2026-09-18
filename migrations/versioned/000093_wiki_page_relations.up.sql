-- Wiki page relations table
-- Stores structured semantic relationships between wiki entity/concept pages.
-- Used for L2 ontology relationship extraction (second-layer ontology).

CREATE TABLE IF NOT EXISTS wiki_page_relations (
    id                  VARCHAR(36) PRIMARY KEY,
    tenant_id           BIGINT NOT NULL,
    knowledge_base_id   VARCHAR(36) NOT NULL,
    source_slug         VARCHAR(255) NOT NULL,
    target_slug         VARCHAR(255) NOT NULL,
    relation_type       VARCHAR(64) NOT NULL,
    relation_label      VARCHAR(128) NOT NULL,
    reverse_label       VARCHAR(128) NOT NULL DEFAULT '',
    description         TEXT NOT NULL DEFAULT '',
    confidence          FLOAT NOT NULL DEFAULT 1.0,
    source_page_type    VARCHAR(32) NOT NULL,
    target_page_type    VARCHAR(32) NOT NULL,
    generated_by        VARCHAR(16) NOT NULL DEFAULT 'pipeline',
    version             INT NOT NULL DEFAULT 1,
    created_at          TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- Query all relations FROM a page
CREATE INDEX IF NOT EXISTS idx_wiki_relations_kb_source
    ON wiki_page_relations (knowledge_base_id, source_slug);

-- Query all relations TO a page (reverse lookup)
CREATE INDEX IF NOT EXISTS idx_wiki_relations_kb_target
    ON wiki_page_relations (knowledge_base_id, target_slug);

-- Uniqueness: one relation per (kb, source, target) direction pair
CREATE UNIQUE INDEX IF NOT EXISTS idx_wiki_relations_kb_source_target
    ON wiki_page_relations (knowledge_base_id, source_slug, target_slug);

-- Filter by relation type
CREATE INDEX IF NOT EXISTS idx_wiki_relations_kb_type
    ON wiki_page_relations (knowledge_base_id, relation_type);
