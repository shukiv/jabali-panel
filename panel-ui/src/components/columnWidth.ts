// columnWidth gives a <Table.Column> its width on the header cell instead of
// through antd's `width` prop. antd puts `width` on the column's <col>, and
// Safari's page zoom multiplies a <col> width a second time: at 250% an 80px
// delete column takes 200px and squeezes the text columns to a few letters
// (GH #2328). A width on the header cell is zoomed once, like the rest of the
// page.
//
//   <Table.Column title="" {...columnWidth(80)} render={...} />
export function columnWidth(width: number) {
  return { onHeaderCell: () => ({ style: { width } }) };
}
