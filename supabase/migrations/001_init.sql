-- ============================================================
-- Forge v0.2  —  Supabase Schema
-- ============================================================
create extension if not exists "uuid-ossp";

-- ── jobs ──────────────────────────────────────────────────────
create table public.jobs (
  id            uuid primary key default uuid_generate_v4(),
  figma_url     text not null,
  repo_url      text,
  platforms     text[]  not null default '{react,kmp}',
  styling       text    not null default 'tailwind',
  threshold     int     not null default 95,
  screen_count  int,
  status        text    not null default 'pending',
  error         text,
  avg_score     float8,
  total_iter    int,
  created_at    timestamptz default now(),
  updated_at    timestamptz default now()
);
create index on public.jobs (status);
create index on public.jobs (created_at desc);

-- ── screens ───────────────────────────────────────────────────
create table public.screens (
  id          uuid primary key default uuid_generate_v4(),
  job_id      uuid not null references public.jobs(id) on delete cascade,
  name        text not null,
  figma_node  text,
  platform    text not null,
  status      text not null default 'pending',
  best_score  float8,
  iterations  int default 0,
  final_code  text,
  created_at  timestamptz default now(),
  updated_at  timestamptz default now()
);
create index on public.screens (job_id, platform);

-- ── iterations ────────────────────────────────────────────────
create table public.iterations (
  id              uuid primary key default uuid_generate_v4(),
  job_id          uuid not null references public.jobs(id) on delete cascade,
  screen_name     text not null,
  platform        text not null,
  iteration       int  not null,
  score           float8 not null,
  layout_score    float8,
  typo_score      float8,
  spacing_score   float8,
  color_score     float8,
  code            text not null,
  screenshot_url  text,
  diff_url        text,
  mismatch_regions jsonb,
  created_at      timestamptz default now()
);
create index on public.iterations (job_id, screen_name, platform);

-- ── events (full audit trail) ─────────────────────────────────
create table public.events (
  id          bigserial primary key,
  job_id      uuid references public.jobs(id),
  routing_key text not null,
  payload     jsonb,
  created_at  timestamptz default now()
);
create index on public.events (job_id);
create index on public.events (routing_key);
create index on public.events (created_at desc);

-- ── Supabase Storage buckets ──────────────────────────────────
-- Run via Supabase dashboard → Storage → New bucket:
-- bucket name: forge-assets   public: true

-- ── Triggers ──────────────────────────────────────────────────
create or replace function _upd() returns trigger language plpgsql as $$
begin new.updated_at = now(); return new; end; $$;

create trigger jobs_upd    before update on public.jobs    for each row execute function _upd();
create trigger screens_upd before update on public.screens for each row execute function _upd();

-- ── RLS ───────────────────────────────────────────────────────
alter table public.jobs        enable row level security;
alter table public.screens     enable row level security;
alter table public.iterations  enable row level security;
alter table public.events      enable row level security;

create policy "service_all" on public.jobs        for all to service_role using (true);
create policy "service_all" on public.screens     for all to service_role using (true);
create policy "service_all" on public.iterations  for all to service_role using (true);
create policy "service_all" on public.events      for all to service_role using (true);

-- ── View ──────────────────────────────────────────────────────
create view public.job_summary as
select
  j.*,
  count(distinct s.id)                                     as total_screens,
  count(s.id) filter (where s.status = 'done')             as done_screens,
  avg(s.best_score)                                        as screens_avg_score,
  json_agg(distinct s.platform) filter (where s.id is not null) as platform_list
from public.jobs j
left join public.screens s on s.job_id = j.id
group by j.id;
