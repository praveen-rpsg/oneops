-- Operational rollback for V002_graph.
-- Kept out of the Atlas migration directory (which holds forward files only).
DROP TABLE IF EXISTS dependency_edge;
