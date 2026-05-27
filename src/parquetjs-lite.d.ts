declare module "parquetjs-lite" {
  export class ParquetSchema {
    constructor(schema: Record<string, { type: string; optional?: boolean }>);
  }

  export class ParquetWriter {
    static openFile(schema: ParquetSchema, path: string): Promise<ParquetWriter>;
    appendRow(row: Record<string, unknown>): Promise<void>;
    close(): Promise<void>;
  }

  export class ParquetReader {
    static openFile(path: string): Promise<ParquetReader>;
    getCursor(): ParquetCursor;
    close(): Promise<void>;
  }

  export class ParquetCursor {
    next(): Promise<Record<string, unknown> | null>;
  }
}
