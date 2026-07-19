// Shared catalog category-filter chip row (JAB-167). CheckableTag chips that
// filter the catalog by tag, with a Clear link when any are active. Used under
// the Catalog tab of all three app marketplaces so they filter identically.
import { Button, Space, Tag } from "antd";

export interface CategoryFilterProps {
  tags: string[];
  selected: string[];
  onChange: (next: string[]) => void;
}

export function CategoryFilter({ tags, selected, onChange }: CategoryFilterProps) {
  if (tags.length === 0) return null;
  const toggle = (t: string) =>
    onChange(selected.includes(t) ? selected.filter((x) => x !== t) : [...selected, t]);
  return (
    <Space size={[8, 8]} wrap style={{ marginBottom: 16 }}>
      {tags.map((t) => (
        <Tag.CheckableTag key={t} checked={selected.includes(t)} onChange={() => toggle(t)}>
          {t}
        </Tag.CheckableTag>
      ))}
      {selected.length > 0 && (
        <Button type="link" size="small" onClick={() => onChange([])}>
          Clear
        </Button>
      )}
    </Space>
  );
}
