-- 为 OpenAI API-key Responses 透传增加分组级首包字段剥离策略。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS openai_passthrough_strip_fields JSONB NOT NULL DEFAULT '["max_output_tokens"]'::jsonb;

COMMENT ON COLUMN groups.openai_passthrough_strip_fields IS
    'OpenAI API-key Responses 透传首包中剥离的 JSON 字段路径；账号 Extra 可整体覆盖';
