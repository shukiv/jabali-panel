// RowDeleteButton — icon-only destructive row action with an AntD
// Popconfirm. Replaces Refine's <DeleteButton> without Refine's
// implicit resource/dataProvider lookup.
//
// Wire the `onConfirm` to a useDeleteMutation(...).mutateAsync({ id })
// in the caller. The Popconfirm owns the "are you sure?" step, so
// callers don't need to run their own Modal unless the delete has
// side-effect options (see UserDeleteAction for that case).
//
// Accessible name is a plain "Delete" — callers that want to scope a
// Playwright getByRole("button", { name: /delete/i }) to a specific
// row should rely on row-level scoping (getByRole("row", { name:
// /email/ }).getByRole("button", ...)) rather than encoding the row
// identity into the button label.
import { DeleteOutlined } from "@icons";
import { Button, Popconfirm } from "antd";
import { feedback } from "../lib/feedback"; // GH #970: themed toasts

interface RowDeleteButtonProps {
  onConfirm: () => Promise<void>;
  /** Text inside the Popconfirm body. Defaults to a generic prompt. */
  confirmTitle?: string;
  /** Override on failure; defaults to showing err.message. */
  onError?: (err: unknown) => void;
  /** Shown in the success toast; defaults to "Deleted". */
  successMessage?: string;
}

export function RowDeleteButton({
  onConfirm,
  confirmTitle = "Delete this record?",
  onError,
  successMessage = "Deleted",
}: RowDeleteButtonProps) {
  const handleConfirm = async () => {
    try {
      await onConfirm();
      feedback.message.success(successMessage);
    } catch (err: unknown) {
      if (onError) {
        onError(err);
        return;
      }
      const msg = err instanceof Error ? err.message : "Delete failed";
      feedback.message.error(msg);
    }
  };

  return (
    <Popconfirm
      title={confirmTitle}
      onConfirm={handleConfirm}
      okText="Delete"
      okButtonProps={{ danger: true }}
      cancelText="Cancel"
    >
      <Button
        variant="filled"
        color="danger"
        icon={<DeleteOutlined />}
        title="Delete"
        aria-label="Delete"
      />
    </Popconfirm>
  );
}
