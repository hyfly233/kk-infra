ALTER TABLE clusters ADD COLUMN IF NOT EXISTS gpu_reservations JSONB NOT NULL DEFAULT '[]'::jsonb;
