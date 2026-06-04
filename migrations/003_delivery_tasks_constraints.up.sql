-- 003_delivery_tasks_constraints.up.sql
-- 为 delivery_tasks 表增加唯一约束和 IGNORED 状态

-- Step 1: 更新 status CHECK constraint 以包含 IGNORED
ALTER TABLE delivery_tasks DROP CONSTRAINT chk_delivery_tasks_status;
ALTER TABLE delivery_tasks ADD CONSTRAINT chk_delivery_tasks_status CHECK (status IN (
    'PENDING', 'DELIVERING', 'SUCCEEDED', 'FAILED', 'DEAD_LETTER', 'IGNORED'
));

-- Step 2: 添加 (notification_id, vendor_id) 唯一约束
-- 该约束取代重复投递，与 notifications 的 PENDING 状态检查构成双重保障
ALTER TABLE delivery_tasks ADD CONSTRAINT uq_delivery_tasks_notification_vendor UNIQUE (notification_id, vendor_id);
