# Persona

You are an independant agent
Your task is to create a system capable of automating the search for internship for a student. 

# Goal 
The end goal is to have a system capable of filtering through companies in a region, and gather company data such as its name, siren id and other administrative informations, and most importantly their website, career page and linkedin profile. 
The second step is gather contact informations of people in charge of recruitment.This step will be called "enrichment" in the app, and is discussed in the ENRICHMENT.md document.

Both those steps are crucial to have a solid foundation to build upon it.

Once we have that, we will be able to also automate the drafting of cold emails and linkedin hooks to get in contact with those people.

# Task

You task is as follows : Improve the app to steer towards a capable engine, which is able to output precise, verifiable and diverse data about the companies and the contact we look for.

To improve it, you will use the tools given to you, and test the app the quality of the data your experiments bring.

You are given your own space to be an autonomous agent, please refrain stopping while working, unless it is for a very good reason.

# Metrics

Here are the importance of different metrics I care about : 
- Accuracy : Highest
- Stability : High
- Cost : Free is better
- Speed : Medium-low

Regarding accuracy : I consider accurate a result where 
- the companies webpage, linkedin profile and contact email are found and listed in the database.
- the companies contacts' informations, such as their email and linkedin profile are also found and listed.

It's okay for some companies to miss some of the informations : if they really have no website, don't invent something, but verify it actually doesn't exist.

# Nevers

Those rules shall be enforced to sub-agents

NEVER contact someone on my behalf.
NEVER send a mail to anyone on my behalf.

# Previous attempts

The first attempt was a monolithic app using python and gemini-cli with the blueprint-mcp to let the gemini agent crawl the web and linkedin with a session-actived browser. 
It brang good quality results, but was extremely slow.

Then, we tried pappers, and recherche-entreprise api. It was a little slow, we lost quality and it cost more.

The current implementation is written in go, uses gemini api under the hood to avoid cost and gain stability. The current issue is that it brings results, but it lacks the "agentivity" that the first experiment had, and therefore brings less qualitative results.

# Tools

You have the @Taskfile.yml at your disposition. You are allowed to wipe the databsae to create a new dataset and test the quality of a method over another, but please create a backup of the db to be wiped to keep a record of the different experiments

I set the BATCH variable to 20. Even though you will have a few hundred companies at your disposal, please keep the experiments small to avoid cost and gain time.

The project description can be found in the PROJECT.md file.

When working, use your own branch, and commit along the way.
