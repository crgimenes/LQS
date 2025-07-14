-- Author: crg@crg.eti.br

local function run_lqs()
  -- Get the start and end positions of the selection
  local start_pos = vim.fn.getpos("'<") -- {buf, line, col, off}
  local end_pos   = vim.fn.getpos("'>")
  local start_line, start_col = start_pos[2], start_pos[3]
  local end_line, end_col     = end_pos[2], end_pos[3]

  -- Enshure that start is really the beginning of the selection
  if start_line > end_line or (start_line == end_line and start_col > end_col) then
    start_line, start_col, end_line, end_col = end_line, end_col, start_line, start_col
  end

  -- Get the selected lines
  local lines = vim.api.nvim_buf_get_lines(0, start_line - 1, end_line, false)
  if #lines == 0 then
    vim.notify("No SQL script selected", vim.log.levels.WARN)
    return
  end

  -- If the selection is in a single line, adjust only that line
  if start_line == end_line then
    lines[1] = string.sub(lines[1], start_col, end_col)
  else
    -- First line: from the start column to the end
    lines[1] = string.sub(lines[1], start_col)
    -- Last line: from the beginning to the end column
    lines[#lines] = string.sub(lines[#lines], 1, end_col)
  end

  local script = table.concat(lines, "\n")
  if script == "" then
    vim.notify("No SQL script selected", vim.log.levels.WARN)
    return
  end

  -- Create a temporary file to store the SQL script
  local tmpfile = vim.fn.tempname() .. ".sql"
  local f = io.open(tmpfile, "w")
  if not f then
    vim.notify("Cannot create temporary file", vim.log.levels.ERROR)
    return
  end
  f:write(script)
  f:close()

  -- Exec the command lqs passing the temporary file
  -- the lqs command must be in the PATH
  local cmd = "lqs " .. tmpfile
  local output = vim.fn.system(cmd)
  os.remove(tmpfile)

  if vim.v.shell_error ~= 0 then
    vim.notify("lqs error: " .. output, vim.log.levels.ERROR)
    return
  end

  -- Show the output in a new buffer
  local buf = vim.api.nvim_create_buf(false, true)
  local out_lines = vim.split(output, "\n")
  vim.api.nvim_buf_set_lines(buf, 0, -1, false, out_lines)
  vim.api.nvim_set_current_buf(buf)
end

vim.api.nvim_create_user_command("LQS", run_lqs, { range = true })

