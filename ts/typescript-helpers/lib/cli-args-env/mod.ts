import { Command as CliffyCommand } from "@cliffy/command";

export interface ArgDef {
  type: string;
  env?: string;
  default?: unknown;
  description?: string;
}

export interface CliArgsEnv {
  name?: string;
  description?: string;
  options: Record<string, ArgDef>;
}

export class Command {
  readonly options: Record<string, unknown> = {};

  #configPathEnv: string;
  #argsEnv: CliArgsEnv;
  #moduleConfig: Record<string, unknown> | null;
  #args: string[];

  constructor(
    configPathEnv: string,
    argsEnv: CliArgsEnv,
    moduleConfig: Record<string, unknown> | null = null,
    args?: string[],
  ) {
    this.#configPathEnv = configPathEnv;
    this.#argsEnv = argsEnv;
    this.#moduleConfig = moduleConfig;
    this.#args = args ?? Deno.args;
  }

  async resolve(): Promise<this> {
    const runtimeConfig = await this.#loadRuntimeConfig();
    const cliVals = await this.#parseCliArgs(this.#argsEnv, this.#args);

    for (const [key, def] of Object.entries(this.#argsEnv.options)) {
      const cliVal = cliVals[key];
      const envVal = def.env ? Deno.env.get(def.env) : undefined;
      const configVal = runtimeConfig?.[key];
      const camelKey = toCamelCase(key);

      if (cliVal !== undefined) {
        this.options[camelKey] = cliVal;
      } else if (envVal !== undefined) {
        this.options[camelKey] = coerce(envVal, def.type);
      } else if (configVal !== undefined) {
        this.options[camelKey] = configVal;
      } else if (def.default !== undefined) {
        this.options[camelKey] = def.default;
      }
    }

    return this;
  }

  async #loadRuntimeConfig(): Promise<Record<string, unknown> | null> {
    const envPath = Deno.env.get(this.#configPathEnv);
    if (envPath) {
      try {
        return JSON.parse(await Deno.readTextFile(envPath));
      } catch {
        return null;
      }
    }
    return this.#moduleConfig;
  }

  async #parseCliArgs(
    argsEnv: CliArgsEnv,
    args: string[],
  ): Promise<Record<string, unknown>> {
    let cmd: any = new CliffyCommand()
      .name(argsEnv.name ?? "server");

    if (argsEnv.description) {
      cmd = cmd.description(argsEnv.description);
    }

    for (const [key, def] of Object.entries(argsEnv.options)) {
      const flag = cliffyFlag(key, def.type);
      cmd = cmd.option(flag, def.description ?? key);
    }

    const { options } = await cmd.parse(args);
    return options;
  }
}

function cliffyFlag(key: string, type: string): string {
  switch (type) {
    case "boolean":
      return `--${key}`;
    case "number":
      return `--${key} <${key}:number>`;
    default:
      return `--${key} <${key}:string>`;
  }
}

function toCamelCase(kebab: string): string {
  return kebab.replace(/-([a-z])/g, (_, c: string) => c.toUpperCase());
}

function coerce(val: string, type: string): unknown {
  switch (type) {
    case "number":
      return Number(val);
    case "boolean":
      return val === "true" || val === "1";
    default:
      return val;
  }
}
