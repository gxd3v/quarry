const ENTITIES = ['customer', 'invoice', 'product', 'shipment', 'warehouse', 'supplier', 'payment', 'ticket']
const CITIES = ['Lisboa', 'Porto', 'Braga', 'Coimbra', 'Faro', 'Aveiro', 'Évora', 'Funchal']

// sqlScript generates a deterministic, realistic PostgreSQL script of at
// least `lines` lines, used to benchmark the editor.
export function sqlScript(lines = 5000): string {
  const out: string[] = []
  for (let i = 0; out.length < lines; i++) out.push(...block(i))
  return out.join('\n') + '\n'
}

function block(i: number): string[] {
  const e = ENTITIES[i % ENTITIES.length]
  const t = `${e}_${i}`
  const city = CITIES[i % CITIES.length]
  const rows = Array.from({ length: 6 }, (_, k) => {
    const id = i * 100 + k
    return `  (${id}, '${e} ${id}', ${(id * 7.31).toFixed(2)}, '${city}', now() - interval '${k} days', ${k % 2 === 0})`
  })
  return [
    `-- ${'='.repeat(70)}`,
    `-- Block ${i}: ${e} reporting for ${city}`,
    `-- ${'='.repeat(70)}`,
    `CREATE TABLE IF NOT EXISTS app.${t} (`,
    `  id          bigint PRIMARY KEY,`,
    `  name        varchar(120) NOT NULL,`,
    `  amount      numeric(12, 2) DEFAULT 0,`,
    `  city        text,`,
    `  created_at  timestamptz NOT NULL DEFAULT now(),`,
    `  active      boolean NOT NULL DEFAULT true,`,
    `  "Payload"   jsonb`,
    `);`,
    ``,
    `CREATE INDEX IF NOT EXISTS ${t}_city_idx ON app.${t} (city, created_at DESC);`,
    ``,
    `INSERT INTO app.${t} (id, name, amount, city, created_at, active) VALUES`,
    rows.join(',\n'),
    `ON CONFLICT (id) DO UPDATE SET amount = EXCLUDED.amount;`,
    ``,
    `/* Monthly totals with a running sum */`,
    `WITH monthly AS (`,
    `  SELECT date_trunc('month', created_at) AS month,`,
    `         sum(amount) AS total,`,
    `         count(*) FILTER (WHERE active) AS active_count`,
    `  FROM app.${t}`,
    `  WHERE city = $1 AND created_at >= $2::date`,
    `  GROUP BY 1`,
    `)`,
    `SELECT m.month,`,
    `       m.total,`,
    `       sum(m.total) OVER (ORDER BY m.month ROWS UNBOUNDED PRECEDING) AS running,`,
    `       coalesce(m.active_count, 0) AS "Active Count"`,
    `FROM monthly m`,
    `LEFT JOIN app.${ENTITIES[(i + 1) % ENTITIES.length]}_${i + 1} o ON o.id = ${i} AND o."Payload" ->> 'kind' = 'x'`,
    `ORDER BY m.month DESC`,
    `LIMIT 100;`,
    ``,
    `UPDATE app.${t} SET active = false, amount = amount * 1.05 WHERE id = $3 RETURNING id, amount;`,
    ``,
    `CREATE OR REPLACE FUNCTION app.${t}_touch() RETURNS trigger LANGUAGE plpgsql AS $$`,
    `BEGIN`,
    `  NEW.created_at := greatest(NEW.created_at, now());`,
    `  IF NEW.amount < 0 THEN`,
    `    RAISE EXCEPTION 'negative amount for %', NEW.id;`,
    `  END IF;`,
    `  RETURN NEW;`,
    `END;`,
    `$$;`,
    ``,
  ]
}
