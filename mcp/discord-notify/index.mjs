import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { z } from "zod";

const WEBHOOK_URL = process.env.DISCORD_WEBHOOK_URL;
if (!WEBHOOK_URL) {
  process.stderr.write("DISCORD_WEBHOOK_URL environment variable is required\n");
  process.exit(1);
}

const server = new McpServer({
  name: "discord-notify",
  version: "1.0.0",
});

server.tool(
  "send_discord_notification",
  "Send a notification message to the project Discord channel",
  { message: z.string().describe("The message to send to Discord") },
  async ({ message }) => {
    const resp = await fetch(WEBHOOK_URL, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ content: message }),
    });

    if (!resp.ok) {
      const body = await resp.text();
      return {
        content: [{ type: "text", text: `Discord API error ${resp.status}: ${body}` }],
        isError: true,
      };
    }

    return {
      content: [{ type: "text", text: "Notification sent to Discord." }],
    };
  }
);

const transport = new StdioServerTransport();
await server.connect(transport);
