-- Rollback wiki_page_relations table

DROP INDEX IF EXISTS idx_wiki_relations_kb_type;
DROP INDEX IF EXISTS idx_wiki_relations_kb_source_target;
DROP INDEX IF EXISTS idx_wiki_relations_kb_target;
DROP INDEX IF EXISTS idx_wiki_relations_kb_source;

DROP TABLE IF EXISTS wiki_page_relations;
