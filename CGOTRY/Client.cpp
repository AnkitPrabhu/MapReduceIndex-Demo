#include "Client.hpp"

Engine::Engine(int NumberOfIsolates){
    v8::V8::InitializeICUDefaultLocation("");
    v8::V8::InitializeExternalStartupData("");
    platform = v8::platform::CreateDefaultPlatform();
    v8::V8::InitializePlatform(platform);
    v8::V8::Initialize();
    this->NumberOfIsolates= NumberOfIsolates;
    for(int i=0;i<NumberOfIsolates;i++){
        v8Instance *w = new v8Instance(platform);
        workers[i]=w;
    }
}

Engine::~Engine(){
    v8::V8::ShutdownPlatform();
}

void* Engine::Route(struct metaData metadoc,const char* doc, std::string filename){
    auto n= isolateNumber++;
    auto index=n%NumberOfIsolates;
    return (void*)workers[index]->Map(metadoc,doc,filename);
}

void Engine::Compile(std::string msg){
    for(int i=0;i<NumberOfIsolates;i++){
        workers[i]->v8WorkLoad(msg);
    }
}
