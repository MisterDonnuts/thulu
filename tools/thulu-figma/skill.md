# thulu-figma: reading Figma designs

**Read-only** access to the workspace's Figma files. Thulu resolves the token
server-side: you'll never see it and you don't need to ask for it. No tool
writes to Figma.

## The order

1. `file_get(file_key)` with the key or with the figma.com link you were given.
   It returns the pages, the frames with their id, and the file's components
   and styles. It carries no geometry: it's the map for picking ids.
2. `nodes_get(file_key, node_ids)` on the frames you're going to build: box,
   auto-layout (mode, gap, padding, alignment), colors in hex, radii, shadows,
   text with its font and the names of the shared styles.
   If the link carries `?node-id=`, passing the link as `file_key` is enough.
3. `images_render(file_key, node_ids)` to look at the frame. PNG and JPG come
   back inline (up to 4 per call).

## How to use it well

- Ask for little and go down one level at a time. A response that passes the
  cap is cut and says so; if that happens, lower `depth` or ask for fewer ids.
- Style names (`styles.fill: "Brand/500"`) and component names
  (`Button/Primary`) are what you should look for in the project's code before
  inventing a new color or component.
- `images_render` with `attach: true` uploads each render to the task, so the
  person reviewing sees what you compared against. Figma URLs expire after 30
  days; attachments don't.
- `comments_list` returns only open threads unless you ask for
  `include_resolved`. Each thread says which node it's pinned to.
- Figma doesn't allow listing a person's teams: `team_projects_list` needs the
  team id, which is in links like `figma.com/files/team/<id>`.

## When it fails

- If it says no Figma token is available, no Figma credential is granted to
  this task. Say so on the task; you can't fix it yourself.
- If it says the tool is turned off for the credential, an operator turned it
  off. Not something to retry either.
- HTTP 403/404 from Figma on a file: the token's owner has no access to that
  file, or the key is wrong.
