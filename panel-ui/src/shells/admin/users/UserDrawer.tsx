// UserDrawer — create + edit drawer for admin Users page.
//
// Replaces the standalone UserCreate / UserEdit page routes with a
// right-side Drawer (matches the docs/CONVENTIONS.md "Drawer for
// create+edit" pattern used by AdminChannelDrawer).
//
// Validation rules mirror the server's so the form rejects early.
// Password on edit is optional — blank means "keep current".
import { Button, Drawer, Form, Grid, Input, Select, Space, Spin, Switch } from "antd";
import { feedback } from "../../../lib/feedback"; // GH #970: themed toasts
import { userLabel } from "./userLabel";
import { useEffect } from "react";

import { CheckOutlined, CloseOutlined } from "@icons";

import { PasswordInput } from "../../../components/PasswordInput";
import {
  useCreateMutation,
  useOneQuery,
  useUpdateMutation,
} from "../../../hooks/useQueries";
import { useSelectQuery } from "../../../hooks/useSelectQuery";
import { noXSS, maxLen } from "../../../utils/validation";

type HostingPackage = {
  id: string;
  name: string;
};

type UserFormInput = {
  email: string;
  username?: string;
  password?: string;
  name_first?: string;
  name_last?: string;
  is_admin: boolean;
  package_id?: string | null;
  webmail_enabled?: boolean;
};

type UserRecord = UserFormInput & {
  id: string;
};

type UserCreated = { id: string };

export interface UserDrawerProps {
  open: boolean;
  onClose: () => void;
  /** Existing user id for edit mode. Undefined = create. */
  editingId?: string;
}

const RESOURCE = "users";

export function UserDrawer({ open, onClose, editingId }: UserDrawerProps) {
  const [form] = Form.useForm<UserFormInput>();
  const screens = Grid.useBreakpoint();
  const isDesktop = screens.lg ?? (typeof window !== "undefined" ? window.innerWidth >= 992 : true);
  const isEdit = Boolean(editingId);

  const { data: existing, isLoading } = useOneQuery<UserRecord>({
    resource: RESOURCE,
    id: editingId,
    enabled: isEdit && open,
  });

  const create = useCreateMutation<UserCreated, UserFormInput>({ resource: RESOURCE });
  const update = useUpdateMutation<UserRecord, UserFormInput>({ resource: RESOURCE });

  useEffect(() => {
    if (!open) return;
    if (isEdit && existing) {
      // Drop password so the edit field stays empty (blank = keep).
      const { password: _pw, ...rest } = existing;
      void _pw;
      form.resetFields();
      // The "Name" field is a single display name bound to name_first, but a
      // user migrated/imported (or created via the API) may have a split
      // name_first + name_last. Show the FULL joined name so editing doesn't
      // silently drop everything after the first word (GH #1227); handleFinish
      // writes it back whole into name_first and clears name_last.
      form.setFieldsValue({
        ...rest,
        name_first: [rest.name_first, rest.name_last].filter(Boolean).join(" "),
      });
    } else if (!isEdit) {
      form.resetFields();
      form.setFieldsValue({ is_admin: false, webmail_enabled: true });
    }
  }, [open, isEdit, existing, form]);

  const handleFinish = async (values: UserFormInput) => {
    try {
      if (isEdit && editingId) {
        const payload = { ...values };
        if (!payload.password) delete payload.password;
        // Backend's *string PATCH body can't tell "field omitted"
        // from "field set to null" (Go JSON unmarshals both to nil),
        // so it treats nil as "don't change". Send "" to mean clear.
        if (payload.package_id == null) {
          payload.package_id = "" as unknown as string;
        }
        // GH #1227: the Name field carries the full display name in name_first;
        // collapse any legacy split by clearing name_last, so the whole name
        // round-trips instead of an orphaned last name reappearing on display.
        payload.name_last = "";
        await update.mutateAsync({ id: editingId, input: payload });
        feedback.message.success("User updated");
      } else {
        await create.mutateAsync(values);
        feedback.message.success("User created");
      }
      onClose();
    } catch (err) {
      feedback.message.error(err instanceof Error ? err.message : "Save failed");
    }
  };

  return (
    <Drawer
      title={isEdit ? (existing ? `Edit ${userLabel(existing)}` : "Edit user") : "Create user"}
      open={open}
      onClose={onClose}
      width={isDesktop ? 520 : undefined}
      placement="right"
      destroyOnClose
    >
      {isEdit && isLoading && !existing ? (
        <div
          style={{
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            minHeight: 240,
          }}
        >
          <Spin />
        </div>
      ) : (
        <Form<UserFormInput>
          form={form}
          layout="vertical"
          initialValues={{ is_admin: false, webmail_enabled: true }}
          onFinish={handleFinish}
        >
          <Form.Item
            label="Email (optional)"
            name="email"
            tooltip="Contact email. Optional — usernames are the login since M54. May be shared across accounts."
            rules={[{ type: "email", message: "Must be a valid email" }]}
          >
            <Input autoComplete={isEdit ? "email" : "off"} />
          </Form.Item>

          {!isEdit && (
            <Form.Item
              label="Username"
              name="username"
              required
              tooltip="Login username (M54) — this is what you sign in with, not your email. Lowercase letters and digits, 3–32 chars, must start with a letter."
              rules={[
                () => ({
                  validator(_, value: string | undefined) {
                    if (!value) {
                      return Promise.reject(new Error("Username is required"));
                    }
                    if (value && !/^[a-z][a-z0-9]{2,31}$/.test(value)) {
                      return Promise.reject(
                        new Error(
                          "3–32 chars, lowercase letters and digits, must start with a letter",
                        ),
                      );
                    }
                    return Promise.resolve();
                  },
                }),
              ]}
            >
              <Input autoComplete="off" placeholder="e.g. alice, dev42" />
            </Form.Item>
          )}

          <Form.Item
            label={isEdit ? "New password" : "Password"}
            name="password"
            tooltip={isEdit ? "Leave blank to keep current password." : "At least 10 characters."}
            rules={
              isEdit
                ? [{ min: 10, message: "At least 10 characters" }]
                : [
                    { required: true, message: "Password is required" },
                    { min: 10, message: "At least 10 characters" },
                  ]
            }
          >
            <PasswordInput autoComplete="new-password" />
          </Form.Item>

          <Form.Item
            label="Name"
            name="name_first"
            tooltip="Display name — a person or a company name."
            rules={[noXSS, maxLen(100)]}
          >
            <Input placeholder="Person or company name" />
          </Form.Item>

          <Form.Item
            name="is_admin"
            label="Admin"
            valuePropName="checked"
            tooltip="Admins can see and manage all users."
          >
            <Switch checkedChildren={<CheckOutlined />} unCheckedChildren={<CloseOutlined />} />
          </Form.Item>

          <Form.Item label="Hosting package" name="package_id">
            <PackageSelect />
          </Form.Item>

          <Form.Item
            name="webmail_enabled"
            label="Webmail client"
            valuePropName="checked"
            tooltip="Turn the Bulwark webmail UI on/off for all of this user's domains. Mail delivery (IMAP/SMTP/JMAP) is unaffected."
          >
            <Switch checkedChildren={<CheckOutlined />} unCheckedChildren={<CloseOutlined />} />
          </Form.Item>

          <Form.Item>
            <Space>
              <Button
                type="primary"
                htmlType="submit"
                loading={create.isPending || update.isPending}
              >
                {isEdit ? "Save" : "Create"}
              </Button>
              <Button onClick={onClose}>Cancel</Button>
            </Space>
          </Form.Item>
        </Form>
      )}
    </Drawer>
  );
}

const PackageSelect = (props: {
  value?: string | null;
  onChange?: (v: string | null) => void;
}) => {
  const { options, isLoading } = useSelectQuery<HostingPackage>({
    resource: "packages",
    labelField: "name",
    valueField: "id",
  });

  return (
    <Select
      placeholder="Select a package (optional)"
      allowClear
      loading={isLoading}
      options={[{ label: "No package", value: null }, ...options]}
      value={props.value}
      onChange={(v: string | null) => props.onChange?.(v ?? null)}
    />
  );
};

// Re-export the type so list pages can avoid duplicating the shape.
export type { UserRecord };
